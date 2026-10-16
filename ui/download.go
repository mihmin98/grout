package ui

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "image/gif"
	_ "image/jpeg"

	"grout/catalog"
	"grout/cfw"
	"grout/download"
	"grout/files"
	"grout/imaging"
	"grout/romm"
	"grout/settings"

	gaba "github.com/BrandonKowalski/gabagool/v2/pkg/gabagool"
	buttons "github.com/BrandonKowalski/gabagool/v2/pkg/gabagool/constants"
	"go.uber.org/atomic"
)

type DownloadInput struct {
	Config         settings.Config
	Host           settings.Host
	Platform       romm.Platform
	SelectedGames  []romm.Rom
	AllGames       []romm.Rom
	SearchFilter   string
	SelectedFileID int
}

type DownloadOutput struct {
	DownloadedGames []romm.Rom
	Platform        romm.Platform
	AllGames        []romm.Rom
	SearchFilter    string
}

type DownloadScreen struct{}

func NewDownloadScreen() *DownloadScreen {
	return &DownloadScreen{}
}

func (s *DownloadScreen) Execute(config settings.Config, host settings.Host, platform romm.Platform, selectedGames []romm.Rom, allGames []romm.Rom, searchFilter string, selectedFileID int) DownloadOutput {
	result, err := s.draw(DownloadInput{
		Config:         config,
		Host:           host,
		Platform:       platform,
		SelectedGames:  selectedGames,
		AllGames:       allGames,
		SelectedFileID: selectedFileID,
		SearchFilter:   searchFilter,
	})
	if err != nil {
		gaba.GetLogger().Error("Download failed", "error", err)
		return DownloadOutput{AllGames: allGames, Platform: platform, SearchFilter: searchFilter}
	}

	if len(result.DownloadedGames) > 0 {
		gaba.GetLogger().Debug("Successfully downloaded games", "count", len(result.DownloadedGames))
	}
	return result
}

func (s *DownloadScreen) draw(input DownloadInput) (DownloadOutput, error) {
	logger := gaba.GetLogger()

	output := DownloadOutput{
		Platform:     input.Platform,
		AllGames:     input.AllGames,
		SearchFilter: input.SearchFilter,
	}

	plan, skipped := download.BuildPlan(input.Config, input.Host, input.Platform, input.SelectedGames, input.SelectedFileID)
	for _, skip := range skipped {
		logger.Warn("Skipping ROM", "game", skip.Game.Name, "id", skip.Game.ID,
			"fs_name", skip.Game.FsName, "reason", skip.Reason)
	}

	// Grout's own Download Complete screen asks about unpacking, so the
	// widget's would only be a second press.
	offer := offersExtraction(input, plan)

	arrived, err := s.fetchRoms(plan, input, offer)
	if err != nil {
		return output, err
	}

	downloaded := gamesThatArrived(input.SelectedGames, arrived)
	logger.Debug("Download complete", "successful", len(downloaded), "attempted", len(input.SelectedGames))
	if len(downloaded) == 0 {
		return output, nil
	}

	archives := arrivedArchives(input, plan, downloaded)
	// A fresh archive replaces what an earlier one was unpacked to. Its entry
	// is pointed at whatever the game launches from once unpacking is settled.
	replaced := make(map[int][]string, len(archives))
	for _, game := range downloaded {
		if location, ok := archives[game.ID]; ok {
			replaced[game.ID] = catalog.ReplaceExtraction(input.Config, game, location)
		}
	}

	extract := make(map[int]bool, len(archives))
	if offer && askToExtract(downloaded, archives) {
		for id := range archives {
			extract[id] = true
		}
	}

	written := s.unpack(input, plan, downloaded, archives, extract)
	repointEntries(downloaded, archives, replaced, written)

	if len(plan.Art) > 0 {
		s.fetchArt(plan.Art, downloaded, input.Host)
	}

	cfw.FillGamesMetadata(plan.Entries)
	catalog.SaveDownloadRecords(download.Records(written, plan.Art, plan.MetadataLocations()))

	output.DownloadedGames = downloaded
	return output, nil
}

// fetchRoms runs the download widget and returns the names that landed.
//
// A partial file left by a failure is deleted: the frontend would list it as a
// game, and grout would count it as already downloaded.
func (s *DownloadScreen) fetchRoms(plan download.Plan, input DownloadInput, skipCompleteScreen bool) (map[string]bool, error) {
	logger := gaba.GetLogger()

	downloads := romDownloads(plan, input.Config.DownloadTimeout.Duration())
	slices.SortFunc(downloads, func(a, b gaba.Download) int {
		return strings.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName))
	})

	result, err := gaba.DownloadManager(downloads, map[string]string{
		"Authorization": input.Host.AuthHeader(),
	}, gaba.DownloadManagerOptions{
		AutoContinueOnComplete: input.Config.DownloadArt || skipCompleteScreen,
		SkipSSLVerification:    input.Host.InsecureSkipVerify,
	})
	if err != nil {
		logger.Error("Error downloading", "error", err)
		if errors.Is(err, gaba.ErrCancelled) {
			for _, item := range downloads {
				files.DeleteFile(item.Location)
			}
		}
		return nil, err
	}

	logger.Debug("Download results", "completed", len(result.Completed), "failed", len(result.Failed))

	for _, failure := range result.Failed {
		logger.Warn("Download failed", "name", failure.Download.DisplayName,
			"url", failure.Download.URL, "error", failure.Error)
		files.DeleteFile(failure.Download.Location)
	}

	arrived := make(map[string]bool, len(result.Completed))
	for _, item := range result.Completed {
		arrived[item.DisplayName] = true
	}
	return arrived, nil
}

// gamesThatArrived keeps the games whose rom file actually landed. The widget
// reports the name the plan gave each download, which is the game's own.
func gamesThatArrived(selected []romm.Rom, arrived map[string]bool) []romm.Rom {
	games := make([]romm.Rom, 0, len(arrived))
	for _, game := range selected {
		if arrived[game.Name] {
			games = append(games, game)
		}
	}
	return games
}

// offersExtraction reports whether this run will ask about unpacking: the
// user keeps archives, the firmware has a 7za, and some game in the plan is an
// archive that may be unpacked.
func offersExtraction(input DownloadInput, plan download.Plan) bool {
	if input.Config.UnzipDownloads || !catalog.CanExtractWith7za() {
		return false
	}
	for _, game := range input.SelectedGames {
		if location := plan.RomLocation(game.Name); location != "" &&
			catalog.ExtractableArchive(game, platformOf(input.Platform, game).FSSlug, location) {
			return true
		}
	}
	return false
}

// arrivedArchives is where each downloaded game that is an unpackable archive
// landed, by ROM ID.
func arrivedArchives(input DownloadInput, plan download.Plan, downloaded []romm.Rom) map[int]string {
	archives := make(map[int]string)
	for _, game := range downloaded {
		location := plan.RomLocation(game.Name)
		if location != "" && catalog.ExtractableArchive(game, platformOf(input.Platform, game).FSSlug, location) {
			archives[game.ID] = location
		}
	}
	return archives
}

// askToExtract is the Download Complete screen when archives arrived: B goes
// back with the archives kept, Y (the games list's Filters button) unpacks
// them all.
func askToExtract(downloaded []romm.Rom, archives map[int]string) bool {
	if len(archives) == 0 {
		return false
	}

	var message string
	if len(archives) == 1 {
		for _, game := range downloaded {
			if _, ok := archives[game.ID]; ok {
				message = localizeWith("download_extract_prompt_one",
					"Download complete.\n{{.Name}} is an archive. Extract it?",
					map[string]any{"Name": game.Name})
			}
		}
	} else {
		message = localizeWith("download_extract_prompt_many",
			"Download complete.\n{{.Count}} games are archives. Extract them?",
			map[string]any{"Count": len(archives)})
	}

	_, err := gaba.ConfirmationMessage(message, []gaba.FooterHelpItem{
		FooterBack(),
		{ButtonName: "Y", HelpText: localize("button_extract", "Extract")},
	}, gaba.MessageOptions{
		ConfirmButton: buttons.VirtualButtonY,
	})
	return err == nil
}

// unpack expands whatever arrived as an archive and points the metadata entry
// at the file that came out. It returns the rom files each game left on the
// card, by ROM ID, for the record of what the download wrote.
//
// A game that fails to unpack keeps its archive and is left out of the
// metadata rather than failing the run, so the rest of the batch still lands.
func (s *DownloadScreen) unpack(input DownloadInput, plan download.Plan, downloaded []romm.Rom, archives map[int]string, extract map[int]bool) map[int]download.Unpacked {
	logger := gaba.GetLogger()
	written := make(map[int]download.Unpacked, len(downloaded))

	for _, game := range downloaded {
		// Where the rom actually landed. A game shipping several versions is
		// downloaded under the name of the one that was picked, so its file
		// list cannot say which.
		location := plan.RomLocation(game.Name)
		if location == "" {
			continue
		}
		_, isArchive := archives[game.ID]

		switch {
		case game.HasMultipleFiles:
			romDirectory := cfw.PlatformRomDirectory(input.Config, platformOf(input.Platform, game).FSSlug)
			unpacked, err := extractWithProgress(game.Name, func(_ context.Context, progress *atomic.Float64) (download.Unpacked, error) {
				return download.ExtractMultiFile(game, romDirectory, progress)
			})
			if err != nil {
				logger.Error("Failed to unpack multi-file ROM", "game", game.Name, "error", err)
				continue
			}
			plan.SetGamePath(game.EntryFileName(), unpacked.Path)
			written[game.ID] = unpacked

		case isArchive && (input.Config.UnzipDownloads || extract[game.ID]):
			unpacked, err := extractWithProgress(game.Name, func(ctx context.Context, progress *atomic.Float64) (download.Unpacked, error) {
				return catalog.UnpackDownloaded(ctx, game, location, progress)
			})
			if err != nil {
				logger.Warn("Failed to unpack ROM, keeping the archive", "game", game.Name, "error", err)
				if !isCancelled(err) {
					showExtractFailure(game.Name, err)
				}
				written[game.ID] = download.Unpacked{Path: location, Written: []string{location}}
				continue
			}
			plan.SetUnpackedGamePath(game.EntryFileName(), unpacked.Path)
			written[game.ID] = unpacked

		default:
			written[game.ID] = download.Unpacked{Path: location, Written: []string{location}}
		}
	}
	return written
}

// repointEntries points each archive game's existing gamelist entry at the
// file it launches from now: the unpacked file, or the archive again when an
// earlier unpacked copy was replaced by it and kept. Done before the metadata
// is written, which then updates that entry instead of adding another.
func repointEntries(downloaded []romm.Rom, archives map[int]string, replaced map[int][]string, written map[int]download.Unpacked) {
	for _, game := range downloaded {
		location, ok := archives[game.ID]
		if !ok {
			continue
		}
		final := written[game.ID].Path
		from := replaced[game.ID]
		if final != location {
			from = append(from, location)
		}
		catalog.RepointEntries(filepath.Dir(location), from, final)
	}
}

func (s *DownloadScreen) fetchArt(items []download.Item, downloaded []romm.Rom, host settings.Host) {
	progress := &atomic.Float64{}

	// One fetcher for the whole run so connections are reused across what can
	// be hundreds of art downloads.
	fetcher := romm.NewArtFetcher(host, romm.DefaultClientTimeout)
	fetcher.Process = imaging.ProcessArtImage

	_, err := gaba.ProcessMessage(
		localize("download_artwork", "Downloading artwork..."),
		gaba.ProcessMessageOptions{
			ShowThemeBackground: true,
			ShowProgressBar:     true,
			Progress:            progress,
		},
		func() (any, error) {
			download.FetchArt(fetcher, items, downloaded, progress)
			return nil, nil
		},
	)
	if err != nil {
		gaba.GetLogger().Warn("Art download process encountered an error", "error", err)
	}
}

// platformOf prefers the game's own platform, which differs from the screen's
// when browsing a collection that spans several.
func platformOf(screen romm.Platform, game romm.Rom) romm.Platform {
	if screen.ID != 0 || game.PlatformID == 0 {
		return screen
	}
	return romm.Platform{ID: game.PlatformID, FSSlug: game.PlatformFSSlug, Name: game.PlatformDisplayName}
}

// romDownloads adapts the plan's rom items to what the download widget takes.
// The plan says what to fetch; only this layer knows the widget's shape.
func romDownloads(plan download.Plan, timeout time.Duration) []gaba.Download {
	out := make([]gaba.Download, 0, len(plan.Roms))
	for _, item := range plan.Roms {
		out = append(out, gaba.Download{
			URL:         item.URL,
			Location:    item.Location,
			DisplayName: item.GameName,
			Timeout:     timeout,
		})
	}
	return out
}
