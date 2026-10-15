package ui

import (
	"context"
	"errors"

	"grout/catalog"
	"grout/download"
	"grout/romm"
	"grout/saves"
	"grout/settings"

	gaba "github.com/BrandonKowalski/gabagool/v2/pkg/gabagool"
	"go.uber.org/atomic"
)

type GameOptionsInput struct {
	Config *settings.Config
	Host   settings.Host
	Game   romm.Rom
	// SlotNames, once SlotsLoaded, is the server's slots from an earlier draw
	// of this panel, so coming back from the QR code does not fetch them again.
	SlotNames   []string
	SlotsLoaded bool
}

type GameOptionsOutput struct {
	Action      GameOptionsAction
	Config      *settings.Config
	Host        settings.Host
	Game        romm.Rom
	NewSlotName string // Set when a new slot is created (for targeted upload)
	SlotNames   []string
	SlotsLoaded bool
}

type GameOptionsScreen struct{}

func NewGameOptionsScreen() *GameOptionsScreen {
	return &GameOptionsScreen{}
}

func (s *GameOptionsScreen) Draw(input GameOptionsInput) (GameOptionsOutput, error) {
	config := input.Config
	output := GameOptionsOutput{Action: GameOptionsActionBack, Config: config, Host: input.Host, Game: input.Game}

	// Fetch save summary to determine available slots
	slotNames := input.SlotNames
	if input.Host.DeviceID != "" && !input.SlotsLoaded {
		client := romm.NewClientFromHost(input.Host, config.ApiTimeout.Duration())
		gaba.ProcessMessage(
			localize("synced_games_loading_detail", "Loading save details..."),
			gaba.ProcessMessageOptions{ShowThemeBackground: true},
			func() (any, error) {
				summary, err := client.GetSaveSummary(input.Game.ID)
				if err == nil {
					slotNames = saves.SlotNames(summary)
				}
				return nil, nil
			},
		)
	}

	output.SlotNames, output.SlotsLoaded = slotNames, true

	oldSlotPref := config.GetSlotPreference(input.Game.ID)

	hasLocalCopy := catalog.HasLocalCopy(*config, input.Game)
	var archivePath string
	var hasArchive bool
	if catalog.CanExtractWith7za() {
		archivePath, hasArchive = catalog.LocalArchive(*config, input.Game)
	}
	items := s.buildMenuItems(config, input.Game, input.Host.DeviceID != "", slotNames, hasLocalCopy, hasArchive)

	title := localize("game_options_title", "Game Options")

	result, err := gaba.OptionsList(
		title,
		gaba.OptionListSettings{
			FooterHelpItems:      OptionsListFooter(),
			InitialSelectedIndex: 0,
			StatusBar:            StatusBar(),
			UseSmallTitle:        true,
		},
		items,
	)

	if err != nil {
		if errors.Is(err, gaba.ErrCancelled) {
			return output, nil
		}
		gaba.GetLogger().Error("Game options screen error", "error", err)
		return output, err
	}

	if result.Action == gaba.ListActionSelected {
		if result.Selected >= 0 && result.Selected < len(result.Items) {
			switch clickedValue(result.Items[result.Selected]) {
			case "show_qr":
				output.Action = GameOptionsActionShowQR
				return output, nil
			case "delete":
				output.Action = GameOptionsActionDelete
				return output, nil
			case "extract":
				output.Action = s.extract(input.Game, archivePath)
				return output, nil
			}
		}
	}

	s.applySettings(config, input.Game, result.Items)

	if err = settings.SaveSlotPreferences(config); err != nil {
		gaba.GetLogger().Error("Error saving slot preferences", "error", err)
		return output, err
	}

	newSlotPref := config.GetSlotPreference(input.Game.ID)
	if newSlotPref != oldSlotPref {
		output.Action = GameOptionsActionSyncNow
		// Check if this is a brand-new slot (not on server yet) for targeted upload
		isNewSlot := true
		for _, name := range slotNames {
			if name == newSlotPref {
				isNewSlot = false
				break
			}
		}
		if isNewSlot {
			output.NewSlotName = newSlotPref
		}
	} else {
		output.Action = GameOptionsActionSaved
	}
	return output, nil
}

// buildMenuItems lists the panel's rows. Extract Archive is offered only when
// the game's archive is on the card and there is a 7za to unpack it with, and
// Delete from Device only when the game has files on the device to remove.
func (s *GameOptionsScreen) buildMenuItems(config *settings.Config, game romm.Rom, deviceRegistered bool, slotNames []string, hasLocalCopy, hasArchive bool) []gaba.ItemWithOptions {
	items := make([]gaba.ItemWithOptions, 0)

	if deviceRegistered {
		saveSlotText := localize("game_options_save_slot", "Save Slot")
		slotOpts := BuildSlotOptions(config, game.ID, slotNames)

		items = append(items, gaba.ItemWithOptions{
			Item:           gaba.MenuItem{Text: saveSlotText},
			Options:        slotOpts.Options,
			SelectedOption: slotOpts.SelectedIdx,
		})
	}

	items = append(items, clickableItem(localize("game_options_show_qr", "Show QR Code"), "show_qr"))
	if hasArchive {
		items = append(items, clickableItem(localize("game_options_extract", "Extract Archive"), "extract"))
	}
	if hasLocalCopy {
		items = append(items, clickableItem(localize("game_options_delete", "Delete from Device"), "delete"))
	}

	return items
}

// extract unpacks the game's archive where it lies. Either way the panel then
// closes back to Game Details, which works out its download label afresh.
func (s *GameOptionsScreen) extract(game romm.Rom, archivePath string) GameOptionsAction {
	_, err := extractWithProgress(game.Name, func(ctx context.Context, progress *atomic.Float64) (download.Unpacked, error) {
		return catalog.ExtractLocalArchive(ctx, game, archivePath, progress)
	})
	if err != nil {
		gaba.GetLogger().Warn("Failed to unpack ROM, keeping the archive", "game", game.Name, "error", err)
		if !isCancelled(err) {
			showExtractFailure(game.Name, err)
		}
		return GameOptionsActionBack
	}
	return GameOptionsActionExtracted
}

func clickableItem(text, value string) gaba.ItemWithOptions {
	return gaba.ItemWithOptions{
		Item:           gaba.MenuItem{Text: text},
		Options:        []gaba.Option{{DisplayName: "", Value: value, Type: gaba.OptionTypeClickable}},
		SelectedOption: 0,
	}
}

// clickedValue is the value of a clickable row, or "" for any other row.
func clickedValue(item gaba.ItemWithOptions) string {
	if item.SelectedOption < 0 || item.SelectedOption >= len(item.Options) {
		return ""
	}
	option := item.Options[item.SelectedOption]
	if option.Type != gaba.OptionTypeClickable {
		return ""
	}
	value, _ := option.Value.(string)
	return value
}

func (s *GameOptionsScreen) applySettings(config *settings.Config, game romm.Rom, items []gaba.ItemWithOptions) {
	saveSlotText := localize("game_options_save_slot", "Save Slot")

	for _, item := range items {
		if item.Item.Text == saveSlotText {
			if item.SelectedOption >= 0 && item.SelectedOption < len(item.Options) {
				selectedOpt := item.Options[item.SelectedOption]
				// Empty string values come from the "New Slot..." keyboard option
				// when the user dismisses the keyboard without typing. Intentionally
				// treated as a no-op so the preference remains unchanged.
				if selectedSlot, ok := selectedOpt.Value.(string); ok && selectedSlot != "" {
					config.SetSlotPreference(game.ID, selectedSlot)
				}
			}
		}
	}
}
