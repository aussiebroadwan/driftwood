package lua

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"log/slog"

	"github.com/bwmarrin/discordgo"
	lua "github.com/yuin/gopher-lua"

	"driftwood/internal/lua/bindings"
	bindings_message "driftwood/internal/lua/bindings/message"
	bindings_options "driftwood/internal/lua/bindings/options"
	bindings_reaction "driftwood/internal/lua/bindings/reaction"
	bindings_state "driftwood/internal/lua/bindings/state"

	"driftwood/internal/lua/utils"
)

// DiscordOptionTypes maps human-readable constants to Discord's option type values.
var DiscordOptionTypes = map[string]int{
	"option_subcommand":       1,
	"option_subcommand_group": 2,
	"option_string":           3,
	"option_integer":          4,
	"option_boolean":          5,
	"option_user":             6,
	"option_channel":          7,
	"option_role":             8,
	"option_mentionable":      9,
	"option_number":           10,
	"option_attachment":       11,
}

// LuaManager handles loading and executing Lua scripts and binding them to Discord commands/events.
type LuaManager struct {
	Bindings     map[string][]bindings.LuaBinding
	OnReadyCbs   []string
	StateManager *utils.StateManager
}

// NewManager creates a new LuaManager with the given session and Guild ID.
func NewManager(session *discordgo.Session, guildID string) *LuaManager {
	sm := utils.NewStateManager()
	manager := &LuaManager{
		StateManager: sm,
		Bindings:     make(map[string][]bindings.LuaBinding),
		OnReadyCbs:   make([]string, 0),
	}

	manager.RegisterBindings(session, guildID)

	// register the bindings
	manager.RegisterDiscordModule()
	return manager
}

// RegisterBindings initializes grouped Lua bindings.
func (m *LuaManager) RegisterBindings(session *discordgo.Session, guildID string) {
	m.Bindings = map[string][]bindings.LuaBinding{
		"default": {
			bindings.NewApplicationCommandBinding(guildID),
			bindings.NewInteractionEventBinding(),
			bindings.NewNewButtonBinding(),
			bindings.NewNewSelectMenuBinding(),
			bindings.NewNewSelectMenuOptionBinding(),
		},
		"timer": {
			bindings.NewRunAfterBinding(),
		},
		"state": {
			bindings_state.NewStateBindingGet(m.StateManager),
			bindings_state.NewStateBindingSet(m.StateManager),
			bindings_state.NewStateBindingClear(m.StateManager),
		},
		"message": {
			bindings_message.NewMessageBindingAdd(),
			bindings_message.NewMessageBindingEdit(),
			bindings_message.NewMessageBindingDelete(),
		},
		"reaction": {
			bindings_reaction.NewReactionBindingAdd(),
			bindings_reaction.NewReactionBindingRemove(),
		},
		"option": {
			bindings_options.NewNewOptionStringBinding(),
			bindings_options.NewNewOptionNumberBinding(),
			bindings_options.NewNewOptionBoolBinding(),
		},
		"channel": {
			bindings.NewChannelBindingGet(guildID),
		},
	}

	slog.Info("Lua bindings registered successfully")
}

// LoadScripts loads all Lua scripts from the directory specified in the `LUA_SCRIPTS_PATH` environment variable.
func (m *LuaManager) LoadScripts(path string) error {
	if path == "" {
		return errors.New("LUA_SCRIPTS_PATH is not set")
	}

	slog.Info("Loading Lua scripts", "path", path)

	// Convert the path to an absolute path.
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path // Fallback to relative path if absolute conversion fails.
	}

	// Update `package.path` to include the new path.
	utils.GetLuaRunner().Do(func(L *lua.LState) {
		packagePath := L.GetField(L.GetGlobal("package"), "path").String()
		newPath := filepath.Join(absPath, "?.lua")
		L.SetField(L.GetGlobal("package"), "path", lua.LString(packagePath+";"+newPath))
	})

	// Walk through the directory and load each Lua script
	err = filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("error walking through Lua scripts: %w", err)
		}

		// Skip directories that are not the entry point (init.lua).
		if info.IsDir() {
			initFilePath := filepath.Join(path, "init.lua")
			if _, err := os.Stat(initFilePath); err == nil {
				slog.Debug("Loading Lua module", "path", initFilePath)
				utils.GetLuaRunner().Do(func(L *lua.LState) {
					if loadErr := L.DoFile(initFilePath); loadErr != nil {
						slog.Error("Failed to load Lua module", "path", initFilePath, "error", loadErr)
					}
				})
			}
			return nil
		}

		// Load single-file commands.
		if filepath.Ext(path) == ".lua" && info.Name() != "init.lua" {
			slog.Debug("Loading Lua script", "path", path)
			utils.GetLuaRunner().Do(func(L *lua.LState) {
				if loadErr := L.DoFile(path); loadErr != nil {
					slog.Error("Failed to load Lua script", "path", path, "error", loadErr)
				}
			})
		}

		return nil
	})

	if err != nil {
		return err
	}

	slog.Info("Lua scripts loaded successfully")
	return nil
}

// RegisterDiscordModule creates a custom loader for `require("driftwood")`
// and injects the actual Go bindings into the Lua state.
func (m *LuaManager) RegisterDiscordModule() {
	// Loader function for `require("driftwood")`.
	discordLoader := func(L *lua.LState) int {
		module := L.NewTable()

		// Add constants to the module.
		for key, value := range DiscordOptionTypes {
			module.RawSetString(key, lua.LNumber(value))
		}

		// Add the on_ready function to the module.
		m.addReady(L, module)

		// Register the function bindings.
		for groupName, group := range m.Bindings {

			if groupName == "default" {
				for _, binding := range group {
					fn := L.NewFunction(binding.Register())
					L.SetField(module, binding.Name(), fn)
					slog.Info("Registered binding", "name", binding.Name())
				}

				continue
			}

			// Create a sub-table for the group.
			subTable := L.NewTable()
			for _, binding := range group {
				fn := L.NewFunction(binding.Register())
				L.SetField(subTable, binding.Name(), fn)
				slog.Info("Registered binding", "name", fmt.Sprintf("%s.%s", groupName, binding.Name()))
			}

			L.SetField(module, groupName, subTable)
		}

		addLogging(L, module)

		L.Push(module)
		return 1
	}

	// Register the loader.
	utils.GetLuaRunner().Do(func(L *lua.LState) {
		L.PreloadModule("driftwood", discordLoader)
	})

}

func (m *LuaManager) addReady(L *lua.LState, module *lua.LTable) {
	L.SetField(module, "on_ready", L.NewFunction(func(L *lua.LState) int {
		handler := L.CheckFunction(1) // First argument is the handler function

		// Create a global function name for the handler
		globalName := fmt.Sprintf("on_ready_handler_%d", time.Now().Unix())

		// Set the Lua function as a global
		L.SetGlobal(globalName, handler)
		m.OnReadyCbs = append(m.OnReadyCbs, globalName)
		return 0
	}))
}

func addLogging(L *lua.LState, module *lua.LTable) {

	logTable := L.NewTable()

	L.SetField(logTable, "debug", L.NewFunction(func(L *lua.LState) int {
		slog.Debug("Lua debug", "message", L.CheckString(1))
		return 0
	}))
	L.SetField(logTable, "info", L.NewFunction(func(L *lua.LState) int {
		slog.Info("Lua info", "message", L.CheckString(1))
		return 0
	}))
	L.SetField(logTable, "error", L.NewFunction(func(L *lua.LState) int {
		slog.Error("Lua error", "message", L.CheckString(1))
		return 0
	}))

	L.SetField(module, "log", logTable)
}

func (m *LuaManager) HandleCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Route the command to the ApplicationCommandBinding.
	for groupIdx := range m.Bindings {
		for idx := range m.Bindings[groupIdx] {
			if m.Bindings[groupIdx][idx].CanHandleInteraction(i) {
				slog.Debug("Binding matched for interaction", "binding", m.Bindings[groupIdx][idx].Name())

				if err := m.Bindings[groupIdx][idx].HandleInteraction(i); err == nil {
					return // Command was handled successfully
				} else {
					slog.Warn("Error handling interaction with binding", "binding", m.Bindings[groupIdx][idx].Name(), "error", err)
				}

			}
		}
	}

	slog.Warn("Command binding not found", "interaction_id", i.ID)
}

func (m *LuaManager) ReadyHandler(s *discordgo.Session, r *discordgo.Ready) {
	slog.Info("Handling ready event")
	m.setSession(s)
	for _, cb := range m.OnReadyCbs {

		utils.GetLuaRunner().Do(func(L *lua.LState) {
			fn := L.GetGlobal(cb)
			if fn == lua.LNil {
				slog.Error("Lua on_ready handler not found", "handler", cb)
				return
			}

			err := L.CallByParam(lua.P{
				Fn:      fn,
				NRet:    0,
				Protect: true,
			})
			if err != nil {
				slog.Error("Error executing Lua on_ready handler", "error", err)
			}
		})

	}
}

func (m *LuaManager) setSession(session *discordgo.Session) {
	for groupIdx := range m.Bindings {
		for idx := range m.Bindings[groupIdx] {
			m.Bindings[groupIdx][idx].SetSession(session)
		}
	}
}
