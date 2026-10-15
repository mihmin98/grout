package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"grout/download"

	gaba "github.com/BrandonKowalski/gabagool/v2/pkg/gabagool"
	"github.com/BrandonKowalski/gabagool/v2/pkg/gabagool/constants"
	"go.uber.org/atomic"
)

// extractWithProgress runs one unpacking behind a progress bar. B stops it:
// 7za is killed, its staging folder cleared and the archive kept. A cancel
// that arrives after the unpacking finished changes nothing, so the result
// is reported as it is.
func extractWithProgress(gameName string, unpack func(context.Context, *atomic.Float64) (download.Unpacked, error)) (download.Unpacked, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	progress := &atomic.Float64{}
	done := make(chan struct{})
	var result download.Unpacked
	var err error

	_, uiErr := gaba.ProcessMessage(
		localizeWith("download_extracting", "Extracting {{.Name}}...", map[string]any{"Name": gameName}),
		gaba.ProcessMessageOptions{
			ShowThemeBackground: true,
			ShowProgressBar:     true,
			Progress:            progress,
			CancelButton:        constants.VirtualButtonB,
			FooterHelpItems:     []gaba.FooterHelpItem{FooterCancel()},
		},
		func() (any, error) {
			defer close(done)
			result, err = unpack(ctx, progress)
			return nil, err
		},
	)

	if errors.Is(uiErr, gaba.ErrCancelled) {
		cancel()
		// The dialog returns at once; the unpacking still has to stop and
		// clear up after itself before anything else touches the folder.
		_, _ = gaba.ProcessMessage(
			localize("extract_cancelling", "Cancelling..."),
			gaba.ProcessMessageOptions{ShowThemeBackground: true},
			func() (any, error) {
				<-done
				return nil, nil
			},
		)
	}
	<-done
	return result, err
}

// isCancelled reports whether an unpacking ended because the user stopped it.
func isCancelled(err error) bool {
	return errors.Is(err, context.Canceled)
}

// showExtractFailure says why an archive could not be unpacked. The archive
// is always kept, so the game is still there to try again or play as it is.
func showExtractFailure(gameName string, err error) {
	gaba.ConfirmationMessage(extractFailureMessage(gameName, err), ContinueFooter(), gaba.MessageOptions{})
}

func extractFailureMessage(gameName string, err error) string {
	var noSpace *download.NoSpaceError
	var noMemory *download.NoMemoryError
	var conflict *download.ConflictError

	switch {
	case errors.As(err, &noSpace):
		return localizeWith("extract_no_space",
			"Not enough space to extract {{.Name}}.\nIt needs {{.Needed}}, the card has {{.Free}} free.\nThe archive was kept.",
			map[string]any{"Name": gameName, "Needed": formatSize(noSpace.Needed), "Free": formatSize(noSpace.Free)})

	case errors.As(err, &noMemory) && noMemory.Available > 0:
		return localizeWith("extract_not_enough_memory",
			"Not enough memory to extract {{.Name}}.\nIt needs about {{.Needed}}, {{.Available}} is free.\nThe archive was kept.",
			map[string]any{"Name": gameName, "Needed": formatSize(noMemory.Needed), "Available": formatSize(noMemory.Available)})

	case errors.As(err, &noMemory):
		return localizeWith("extract_ran_out_of_memory",
			"Ran out of memory extracting {{.Name}}.\nThe archive was kept.",
			map[string]any{"Name": gameName})

	case errors.As(err, &conflict):
		return localizeWith("extract_conflict",
			"Could not extract {{.Name}}:\n{{.File}} is already on the card.\nThe archive was kept.",
			map[string]any{"Name": gameName, "File": filepath.Base(conflict.Path)})
	}

	return localizeWith("extract_failed_kept_archive",
		"Could not extract {{.Name}}.\nThe archive was kept. Check the logs for more info.",
		map[string]any{"Name": gameName})
}

// formatSize renders a byte count the way a card's capacity is talked about.
func formatSize(bytes uint64) string {
	const (
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case bytes >= gib:
		return fmt.Sprintf("%.1f GB", float64(bytes)/gib)
	case bytes >= mib:
		return fmt.Sprintf("%.0f MB", float64(bytes)/mib)
	default:
		return fmt.Sprintf("%.0f KB", float64(bytes)/1024)
	}
}
