package utils

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
	lua "github.com/yuin/gopher-lua"
)

// ParseComponents parses a Lua table into Discord message components.
func ParseComponents(_ *lua.LState, table *lua.LTable) ([]discordgo.MessageComponent, error) {
	var components []discordgo.MessageComponent

	table.ForEach(func(_, value lua.LValue) {
		componentTable, ok := value.(*lua.LTable)
		if !ok {
			return // Skip invalid entries
		}

		componentType := componentTable.RawGetString("type").String()
		switch componentType {
		case "button":
			label := componentTable.RawGetString("label").String()
			customID := componentTable.RawGetString("custom_id").String()

			disabled := false
			disabledRaw := componentTable.RawGetString("disabled")
			if disabledRaw.Type() == lua.LTBool {
				disabled = lua.LVAsBool(disabledRaw)
			}

			components = append(components, discordgo.Button{
				Label:    label,
				CustomID: customID,
				Style:    discordgo.PrimaryButton, // Default style
				Disabled: disabled,
			})
		case "select":
			placeholder := componentTable.RawGetString("placeholder").String()
			customID := componentTable.RawGetString("custom_id").String()

			disabled := false
			disabledRaw := componentTable.RawGetString("disabled")
			if disabledRaw.Type() == lua.LTBool {
				disabled = lua.LVAsBool(disabledRaw)
			}

			optionsRaw := componentTable.RawGetString("options")
			options, ok := optionsRaw.(*lua.LTable)
			if !ok {
				return // Skip invalid entries
			}

			var selectOptions []discordgo.SelectMenuOption
			options.ForEach(func(_, value lua.LValue) {
				optionTable, ok := value.(*lua.LTable)
				if !ok {
					return // Skip invalid entries
				}

				optLabel := optionTable.RawGetString("label").String()
				optValue := optionTable.RawGetString("value").String() // custom id

				selectOptions = append(selectOptions, discordgo.SelectMenuOption{
					Label: optLabel,
					Value: optValue,
				})
			})

			components = append(components, discordgo.SelectMenu{
				Placeholder: placeholder,
				CustomID:    customID,
				Options:     selectOptions,
				Disabled:    disabled,
			})

		default:
			return
		}
	})

	// Wrap components in an action row
	if len(components) > 0 {
		return []discordgo.MessageComponent{
			discordgo.ActionsRow{Components: components},
		}, nil
	}

	return nil, fmt.Errorf("no valid components found")
}
