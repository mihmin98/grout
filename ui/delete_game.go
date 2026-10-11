package ui

import (
	"errors"
	"strings"

	"grout/catalog"
	"grout/romm"
	"grout/settings"

	gaba "github.com/BrandonKowalski/gabagool/v2/pkg/gabagool"
	buttons "github.com/BrandonKowalski/gabagool/v2/pkg/gabagool/constants"
)

type DeleteGameInput struct {
	Config *settings.Config
	Game   romm.Rom
}

type DeleteGameOutput struct {
	Action DeleteGameAction
	Game   romm.Rom
}

// DeleteGameScreen removes a downloaded game's files from the device after
// asking. Saves are never deleted; catalog.PlanDeletion keeps them.
type DeleteGameScreen struct{}

func NewDeleteGameScreen() *DeleteGameScreen {
	return &DeleteGameScreen{}
}

func (s *DeleteGameScreen) Draw(input DeleteGameInput) (DeleteGameOutput, error) {
	output := DeleteGameOutput{Action: DeleteGameActionCancel, Game: input.Game}
	logger := gaba.GetLogger()

	plan, err := catalog.PlanDeletion(*input.Config, input.Game)
	if err != nil {
		logger.Error("Could not plan deleting a game", "rom", input.Game.ID, "error", err)
		showDeleteMessage(localize("delete_game_plan_failed", "Could not work out what to delete.\nCheck the logs for more info."))
		return output, nil
	}

	// Everything is already gone. Carrying out the plan still forgets the
	// game's rows, so the option disappears.
	if len(plan.Files) == 0 && len(plan.Meta) == 0 {
		catalog.ExecuteDeletion(plan)
		showDeleteMessage(deleteNothingMessage(input.Game.Name))
		output.Action = DeleteGameActionDone
		return output, nil
	}

	confirmed, err := confirmDeletion(deleteConfirmMessage(plan))
	if err != nil || !confirmed {
		return output, err
	}

	report, _ := gaba.ProcessMessage(
		localize("delete_game_progress", "Deleting..."),
		gaba.ProcessMessageOptions{ShowThemeBackground: true},
		func() (catalog.DeleteReport, error) {
			return catalog.ExecuteDeletion(plan), nil
		},
	)

	showDeleteMessage(deleteResultMessage(input.Game.Name, report))
	output.Action = DeleteGameActionDone
	return output, nil
}

// confirmDeletion asks as logout does: B cancels, X confirms.
func confirmDeletion(message string) (bool, error) {
	_, err := gaba.ConfirmationMessage(
		message,
		[]gaba.FooterHelpItem{
			{ButtonName: "B", HelpText: localize("button_cancel", "Cancel")},
			{ButtonName: "X", HelpText: localize("button_confirm", "Confirm")},
		},
		gaba.MessageOptions{
			ConfirmButton: buttons.VirtualButtonX,
		},
	)
	if err != nil {
		if errors.Is(err, gaba.ErrCancelled) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// showDeleteMessage shows a message until the user continues.
func showDeleteMessage(message string) {
	gaba.ConfirmationMessage(message, ContinueFooter(), gaba.MessageOptions{})
}

// deleteConfirmMessage is the question asked before deleting, with a warning
// when what to delete was inferred rather than recorded.
func deleteConfirmMessage(plan catalog.DeletePlan) string {
	lines := []string{localizeWith("delete_game_confirm", "Delete {{.Name}} from this device? Saves are kept.",
		map[string]any{"Name": plan.Game.Name})}
	if plan.Legacy {
		lines = append(lines, localize("delete_game_confirm_legacy",
			"It was downloaded before Grout kept a record of its files; some files may stay."))
	}
	return strings.Join(lines, "\n")
}

func deleteNothingMessage(name string) string {
	return localizeWith("delete_game_nothing", "{{.Name}} has nothing left on this device to delete.",
		map[string]any{"Name": name})
}

// deleteResultMessage says what a deletion did. Files left in a game's folder
// (saves and states RetroArch wrote beside it) count as kept, alongside the
// recorded files the plan refused to touch. A metadata entry that could not
// be removed counts as a failure.
func deleteResultMessage(name string, report catalog.DeleteReport) string {
	lines := []string{localizeWith("delete_game_done", "Deleted {{.Name}}.", map[string]any{"Name": name})}

	kept := len(report.Kept)
	for _, count := range report.KeptDirs {
		kept += count
	}
	if kept > 0 {
		lines = append(lines, localizeWith("delete_game_kept",
			"Kept {{.Count}} file(s): saves and files Grout didn't download.", map[string]any{"Count": kept}))
	}

	failed := len(report.Failed)
	if report.MetadataErr != nil {
		failed++
	}
	if failed > 0 {
		lines = append(lines, localizeWith("delete_game_failed",
			"{{.Count}} could not be deleted; see the log.", map[string]any{"Count": failed}))
	}

	return strings.Join(lines, "\n")
}
