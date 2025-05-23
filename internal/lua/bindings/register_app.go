package bindings

import (
	"driftwood/internal/lua/utils"
	"fmt"
	"log/slog"

	"github.com/bwmarrin/discordgo"
	lua "github.com/yuin/gopher-lua"
)

// ApplicationCommandBinding manages the /application_command registration in Lua.
type ApplicationCommandBinding struct {
	Session  *discordgo.Session
	GuildID  string
	Commands map[string]string // Maps command names to Lua global handler names

	waitRegister []func(*discordgo.Session)
}

// NewApplicationCommandBinding initializes a new ApplicationCommandBinding.
func NewApplicationCommandBinding(guildID string) *ApplicationCommandBinding {
	slog.Debug("Creating new ApplicationCommandBinding")
	return &ApplicationCommandBinding{
		GuildID:      guildID,
		Commands:     make(map[string]string),
		waitRegister: []func(*discordgo.Session){},
	}
}

// Name returns the name of the Lua global table for this binding.
func (b *ApplicationCommandBinding) Name() string {
	return "register_application_command"
}

func (b *ApplicationCommandBinding) SetSession(session *discordgo.Session) {
	slog.Info("Setting session for ApplicationCommandBinding")
	b.Session = session

	for _, f := range b.waitRegister {
		f(session)
	}
}

// Register adds the `register_application_command` function to a Lua table.
func (b *ApplicationCommandBinding) Register() lua.LGFunction {
	slog.Info("Registering application command Lua function")
	return func(L *lua.LState) int {
		command := L.CheckTable(1)

		// Validate required fields
		name := command.RawGetString("name")
		if name.Type() != lua.LTString {
			L.ArgError(1, "'name' must be a string")
		}

		description := command.RawGetString("description")
		if description.Type() != lua.LTString {
			L.ArgError(1, "'description' must be a string")
		}

		handler := command.RawGetString("handler")
		if handler != lua.LNil && handler.Type() != lua.LTFunction {
			L.ArgError(1, "'handler' must be a function if provided")
		}

		options := command.RawGetString("options")
		if options != lua.LNil && options.Type() != lua.LTTable {
			L.ArgError(1, "'options' must be a table if provided")
		}

		globalName := fmt.Sprintf("handler_%s", name)
		if handler != lua.LNil {
			L.SetGlobal(globalName, handler)
			b.Commands[name.String()] = globalName
		}

		commandOptions := []*discordgo.ApplicationCommandOption{}
		if options != lua.LNil {
			commandOptions = b.parseOptions(L, name.String(), options.(*lua.LTable))
		}

		appCmd := &discordgo.ApplicationCommand{
			Name:        name.String(),
			Description: description.String(),
			Options:     commandOptions,
		}

		if b.Session == nil {
			b.waitRegister = append(b.waitRegister, func(session *discordgo.Session) {
				if _, err := session.ApplicationCommandCreate(session.State.User.ID, b.GuildID, appCmd); err != nil {
					L.RaiseError("failed to register command '%s' with Discord: %s", name, err.Error())
				}
			})
			return 0
		}

		if _, err := b.Session.ApplicationCommandCreate(b.Session.State.User.ID, b.GuildID, appCmd); err != nil {
			L.RaiseError("failed to register command '%s' with Discord: %s", name, err.Error())
		}

		slog.Info("Registered command successfully", "name", name, "description", description)
		return 0
	}
}

// parseOptions parses Lua options tables recursively to support subcommands.
func (b *ApplicationCommandBinding) parseOptions(L *lua.LState, parentName string, options *lua.LTable) []*discordgo.ApplicationCommandOption {
	var commandOptions []*discordgo.ApplicationCommandOption

	options.ForEach(func(_, value lua.LValue) {
		if optTable, ok := value.(*lua.LTable); ok {
			// Validate option fields
			name := optTable.RawGetString("name")
			if name.Type() != lua.LTString {
				L.ArgError(1, "'name' in options must be a string")
			}

			description := optTable.RawGetString("description")
			if description.Type() != lua.LTString {
				L.ArgError(1, "'description' in options must be a string")
			}

			typeField := optTable.RawGetString("type")
			if typeField.Type() != lua.LTNumber {
				L.ArgError(1, "'type' in options must be a number")
			}

			option := &discordgo.ApplicationCommandOption{
				Name:        name.String(),
				Description: description.String(),
				Type:        discordgo.ApplicationCommandOptionType(uint8(typeField.(lua.LNumber))),
				Required:    lua.LVAsBool((optTable.RawGetString("required"))),
			}

			if option.Type == discordgo.ApplicationCommandOptionSubCommand {
				handler := optTable.RawGetString("handler")
				if handler.Type() != lua.LTFunction {
					L.ArgError(1, "Subcommand '%s' must have a 'handler' function")
					return
				}

				handlerName := fmt.Sprintf("handler_%s_%s", parentName, option.Name)
				L.SetGlobal(handlerName, handler)
				b.Commands[parentName+"_"+option.Name] = handlerName

				if subOptions := optTable.RawGetString("options"); subOptions.Type() == lua.LTTable {
					option.Options = b.parseOptions(L, parentName+"_"+option.Name, subOptions.(*lua.LTable))
				}
			}

			commandOptions = append(commandOptions, option)
			slog.Debug("Parsed command option", "name", option.Name, "description", option.Description, "type", option.Type)
		}
	})

	return commandOptions
}

func (b *ApplicationCommandBinding) CanHandleInteraction(interaction *discordgo.InteractionCreate) bool {
	return interaction.Type == discordgo.InteractionApplicationCommand
}

// HandleInteraction executes the Lua handler for a command or subcommand.
func (b *ApplicationCommandBinding) HandleInteraction(interaction *discordgo.InteractionCreate) error {
	slog.Info("Handling command interaction", "interaction_id", interaction.ID)
	data := interaction.ApplicationCommandData()
	commandName := data.Name

	for _, opt := range data.Options {
		if opt.Type == discordgo.ApplicationCommandOptionSubCommand {
			commandName += "_" + opt.Name
			break
		}
	}

	globalName, exists := b.Commands[commandName]
	if !exists {
		slog.Warn("Command not registered", "command", commandName)
		return fmt.Errorf("command '%s' not registered", commandName)
	}

	utils.GetLuaRunner().Do(func(L *lua.LState) {
		slog.Debug("Executing Lua handler", "handler_name", globalName)
		fn := L.GetGlobal(globalName)
		if fn == lua.LNil {
			slog.Error("Lua handler not implemented", "command", commandName)
			return
		}

		interactionTable := b.prepareInteractionTable(L, interaction)

		err := L.CallByParam(lua.P{
			Fn:      fn,
			NRet:    0,
			Protect: true,
		}, interactionTable)
		if err != nil {
			slog.Error("Error executing Lua command handler", "error", err, "command", commandName)
			return
		}
		slog.Info("Command handled successfully", "command", commandName)
	})

	return nil
}

// prepareInteractionTable prepares a Lua table containing interaction details.
func (b *ApplicationCommandBinding) prepareInteractionTable(L *lua.LState, interaction *discordgo.InteractionCreate) *lua.LTable {
	interactionTable := utils.PrepareInteractionTable(L, b.Session, interaction)
	interactionTable.RawSetString("options", b.buildOptionsTable(L, nil, interaction.ApplicationCommandData().Options))
	return interactionTable
}

// buildOptionsTable recursively builds a Lua table from Discord interaction options.
func (b *ApplicationCommandBinding) buildOptionsTable(L *lua.LState, T *lua.LTable, options []*discordgo.ApplicationCommandInteractionDataOption) *lua.LTable {
	if T == nil {
		T = L.NewTable()
	}

	for _, opt := range options {
		if opt.Type == discordgo.ApplicationCommandOptionSubCommand {
			if opt.Options != nil {
				return b.buildOptionsTable(L, T, opt.Options)
			}
		} else {
			switch opt.Type {
			case discordgo.ApplicationCommandOptionInteger:
				T.RawSetString(opt.Name, lua.LNumber(opt.IntValue()))
			case discordgo.ApplicationCommandOptionBoolean:
				T.RawSetString(opt.Name, lua.LBool(opt.BoolValue()))
			case discordgo.ApplicationCommandOptionString:
				T.RawSetString(opt.Name, lua.LString(opt.StringValue()))
			case discordgo.ApplicationCommandOptionNumber:
				T.RawSetString(opt.Name, lua.LNumber(opt.FloatValue()))
			default:
				T.RawSetString(opt.Name, lua.LString(fmt.Sprintf("%v", opt.Value)))
			}
		}
	}
	return T
}
