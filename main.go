//go:generate go install -v github.com/kevinburke/go-bindata/v4/go-bindata
//go:generate go-bindata -prefix res/ -pkg assets -o assets/assets.go res/FirefoxDeveloperEdition.lnk
//go:generate go install -v github.com/josephspurrier/goversioninfo/cmd/goversioninfo
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/portapps/phyrox-developer-portable/assets"
	"github.com/portapps/portapps/v3"
	"github.com/portapps/portapps/v3/pkg/log"
	"github.com/portapps/portapps/v3/pkg/mutex"
	"github.com/portapps/portapps/v3/pkg/shortcut"
	"github.com/portapps/portapps/v3/pkg/utl"
	"github.com/portapps/portapps/v3/pkg/win"
)

type config struct {
	Profile              string `yaml:"profile" mapstructure:"profile"`
	MultipleInstances    bool   `yaml:"multiple_instances" mapstructure:"multiple_instances"`
	DisableTelemetry     bool   `yaml:"disable_telemetry" mapstructure:"disable_telemetry"`
	DisableCrashReporter bool   `yaml:"disable_crash_reporter" mapstructure:"disable_crash_reporter"`
	Cleanup              bool   `yaml:"cleanup" mapstructure:"cleanup"`
}

var (
	app *portapps.App
	cfg *config
)

func init() {
	var err error

	// Default config
	cfg = &config{
		Profile:              "default",
		MultipleInstances:    false,
		DisableTelemetry:     false,
		DisableCrashReporter: true,
		Cleanup:              false,
	}

	// Init app
	if app, err = portapps.NewWithCfg("phyrox-developer-portable", "Phyrox Developer Edition", cfg); err != nil {
		log.Fatal().Err(err).Msg("Cannot initialize application. See log file for more info.")
	}
}

func main() {
	utl.CreateFolder(app.DataPath)
	profileFolder := utl.CreateFolder(app.DataPath, "profile", cfg.Profile)

	app.Process = filepath.Join(app.AppPath, "firefox.exe")
	app.Args = []string{
		"-profile",
		profileFolder,
	}

	// Set env vars
	crashreporterFolder := utl.CreateFolder(app.DataPath, "crashreporter")
	pluginsFolder := utl.CreateFolder(app.DataPath, "plugins")
	os.Setenv("MOZ_CRASHREPORTER_DATA_DIRECTORY", crashreporterFolder)
	os.Setenv("MOZ_MAINTENANCE_SERVICE", "0")
	os.Setenv("MOZ_PLUGIN_PATH", pluginsFolder)
	os.Setenv("MOZ_UPDATER", "0")
	if cfg.DisableCrashReporter {
		os.Setenv("MOZ_CRASHREPORTER", "0")
		os.Setenv("MOZ_CRASHREPORTER_DISABLE", "1")
		os.Setenv("MOZ_CRASHREPORTER_NO_REPORT", "1")
	}
	if cfg.DisableTelemetry {
		os.Setenv("MOZ_DATA_REPORTING", "0")
	}

	// Create and check mutex
	mu, err := mutex.Create(app.ID)
	if err != nil {
		if !cfg.MultipleInstances {
			log.Error().Msg("You have to enable multiple instances in your configuration if you want to launch another instance")
			if _, err = win.MsgBox(
				fmt.Sprintf("%s portable", app.Name),
				"Other instance detected. You have to enable multiple instances in your configuration if you want to launch another instance.",
				win.MsgBoxBtnOk|win.MsgBoxIconError); err != nil {
				log.Error().Err(err).Msg("Cannot create dialog box")
			}
			return
		} else {
			log.Warn().Msg("Another instance is already running")
		}
	} else {
		defer mutex.Release(mu)
	}

	// Cleanup on exit
	if cfg.Cleanup {
		defer func() {
			utl.Cleanup([]string{
				filepath.Join(os.Getenv("APPDATA"), "Mozilla", "Firefox"),
				filepath.Join(os.Getenv("LOCALAPPDATA"), "Mozilla", "Firefox"),
				filepath.Join(os.Getenv("USERPROFILE"), "AppData", "LocalLow", "Mozilla"),
			})
		}()
	}

	// Multiple instances
	if cfg.MultipleInstances {
		log.Info().Msg("Multiple instances enabled")
		app.Args = append(app.Args, "-no-remote")
	}

	// Policies
	if err := createPolicies(); err != nil {
		log.Fatal().Err(err).Msg("Cannot create policies")
	}

	// Fix extensions path
	if err := updateAddonStartup(profileFolder); err != nil {
		log.Error().Err(err).Msg("Cannot fix extensions path")
	}

	// Copy default shortcut
	shortcutPath := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Phyrox Developer Edition Portable.lnk")
	defaultShortcut, err := assets.Asset("FirefoxDeveloperEdition.lnk")
	if err != nil {
		log.Error().Err(err).Msg("Cannot load asset FirefoxDeveloperEdition.lnk")
	}
	err = os.WriteFile(shortcutPath, defaultShortcut, 0644)
	if err != nil {
		log.Error().Err(err).Msg("Cannot write default shortcut")
	}

	// Update default shortcut
	err = shortcut.Create(shortcut.Shortcut{
		ShortcutPath:     shortcutPath,
		TargetPath:       app.Process,
		Arguments:        shortcut.Property{Clear: true},
		Description:      shortcut.Property{Value: "Phyrox Developer Edition Portable by Portapps"},
		IconLocation:     shortcut.Property{Value: app.Process},
		WorkingDirectory: shortcut.Property{Value: app.AppPath},
	})
	if err != nil {
		log.Error().Err(err).Msg("Cannot create shortcut")
	}
	defer func() {
		if err := os.Remove(shortcutPath); err != nil {
			log.Error().Err(err).Msg("Cannot remove shortcut")
		}
	}()

	defer app.Close()
	app.Launch(os.Args[1:])
}

func createPolicies() error {
	appFile := filepath.Join(utl.CreateFolder(app.AppPath, "distribution"), "policies.json")
	dataFile := filepath.Join(app.DataPath, "policies.json")
	jsonPolicies := map[string]interface{}{
		"policies": map[string]interface{}{},
	}
	defaultPolicies, err := json.Marshal(jsonPolicies)
	if err != nil {
		return errors.Wrap(err, "Cannot marshal default policies")
	}
	log.Debug().Msgf("Default policies: %s", string(defaultPolicies))

	if utl.Exists(dataFile) {
		rawCustomPolicies, err := os.ReadFile(dataFile)
		if err != nil {
			return errors.Wrap(err, "Cannot read custom policies")
		}

		if err := json.Unmarshal(rawCustomPolicies, &jsonPolicies); err != nil {
			return errors.Wrap(err, "Cannot consume custom policies")
		}
		customPolicies, err := json.Marshal(jsonPolicies)
		if err != nil {
			return errors.Wrap(err, "Cannot marshal custom policies")
		}
		log.Debug().Msgf("Custom policies: %s", string(customPolicies))
	}

	policies, ok := jsonPolicies["policies"].(map[string]interface{})
	if !ok {
		if _, exists := jsonPolicies["policies"]; exists {
			return errors.New("policies must be an object")
		}
		policies = map[string]interface{}{}
		jsonPolicies["policies"] = policies
	}
	policies["DisableAppUpdate"] = true
	policies["DontCheckDefaultBrowser"] = true
	if cfg.DisableTelemetry {
		policies["DisableFirefoxStudies"] = true
		policies["DisableTelemetry"] = true
	}

	appliedPolicies, err := json.MarshalIndent(jsonPolicies, "", "  ")
	if err != nil {
		return errors.Wrap(err, "Cannot marshal policies")
	}
	log.Debug().Msgf("Applied policies: %s", string(appliedPolicies))
	if err := os.WriteFile(appFile, appliedPolicies, 0644); err != nil {
		return errors.Wrap(err, "Cannot write policies")
	}

	return nil
}

func updateAddonStartup(profileFolder string) error {
	lz4File := filepath.Join(profileFolder, "addonStartup.json.lz4")
	if !utl.Exists(lz4File) || app.Prev.RootPath == "" {
		return nil
	}

	lz4Raw, err := mozLz4Decompress(lz4File)
	if err != nil {
		return err
	}

	prevPathLin := strings.Replace(utl.FormatUnixPath(app.Prev.RootPath), ` `, `%20`, -1)
	currPathLin := strings.Replace(utl.FormatUnixPath(app.RootPath), ` `, `%20`, -1)
	lz4Str := strings.Replace(string(lz4Raw), prevPathLin, currPathLin, -1)

	prevPathWin := strings.Replace(strings.Replace(utl.FormatWindowsPath(app.Prev.RootPath), `\`, `\\`, -1), ` `, `%20`, -1)
	currPathWin := strings.Replace(strings.Replace(utl.FormatWindowsPath(app.RootPath), `\`, `\\`, -1), ` `, `%20`, -1)
	lz4Str = strings.Replace(lz4Str, prevPathWin, currPathWin, -1)

	lz4Enc, err := mozLz4Compress([]byte(lz4Str))
	if err != nil {
		return err
	}

	return os.WriteFile(lz4File, lz4Enc, 0644)
}
