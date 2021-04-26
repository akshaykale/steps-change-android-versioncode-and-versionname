package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-utils/command"
	"github.com/bitrise-io/go-utils/fileutil"
	"github.com/bitrise-io/go-utils/log"
)

const (
	// versionCode — A positive integer [...] -> https://developer.android.com/studio/publish/versioning
	versionCodeRegexPattern = `^versionCode(?:\s|=)+(.*?)(?:\s|\/\/|$)`
	// versionName — A string used as the version number shown to users [...] -> https://developer.android.com/studio/publish/versioning
	versionNameRegexPattern = `^versionName(?:=|\s)+(.*?)(?:\s|\/\/|$)`
)

type config struct {
	BuildGradlePth    string `env:"build_gradle_path,file"`
	VersionNameSep    string `env:"version_name_seperator"`
	VersionNameSuffix string `env:"version_name_suffix"`
	NewVersionCode    int    `env:"new_version_code,range]0..2100000000]"`
	VersionCodeOffset int    `env:"version_code_offset"`
}

type updateFn func(line string, lineNum int, matches []string) string

func findAndUpdate(reader io.Reader, update map[*regexp.Regexp]updateFn) (string, error) {
	scanner := bufio.NewScanner(reader)
	var updatedLines []string

	for lineNum := 0; scanner.Scan(); lineNum++ {
		line := scanner.Text()

		updated := false
		for re, fn := range update {
			if match := re.FindStringSubmatch(strings.TrimSpace(line)); len(match) == 2 {
				if updatedLine := fn(line, lineNum, match); updatedLine != "" {
					updatedLines = append(updatedLines, updatedLine)
					updated = true
					break
				}
			}
		}
		if !updated {
			updatedLines = append(updatedLines, line)
		}
	}

	return strings.Join(updatedLines, "\n"), scanner.Err()
}

func exportOutputs(outputs map[string]string) error {
	for envKey, envValue := range outputs {
		cmd := command.New("envman", "add", "--key", envKey, "--value", envValue)
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

func failf(format string, v ...interface{}) {
	log.Errorf(format, v...)
	os.Exit(1)
}

// BuildGradleVersionUpdater updates versionName and versionCode in the given build.gradle file.
type BuildGradleVersionUpdater struct {
	buildGradleReader io.Reader
}

// NewBuildGradleVersionUpdater constructs a new BuildGradleVersionUpdater.
func NewBuildGradleVersionUpdater(buildGradleReader io.Reader) BuildGradleVersionUpdater {
	return BuildGradleVersionUpdater{buildGradleReader: buildGradleReader}
}

// UpdateResult stors the result of the version update.
type UpdateResult struct {
	NewContent          string
	FinalVersionCode    string
	FinalVersionName    string
	RealVersionName     string
	UpdatedVersionCodes int
	UpdatedVersionNames int
}

// UpdateVersion executes the version updates.
func (u BuildGradleVersionUpdater) UpdateVersion(newVersionCode, versionCodeOffset int, versionNameSep, versionNameSuffix string) (UpdateResult, error) {
	res := UpdateResult{}
	var err error

	res.NewContent, err = findAndUpdate(u.buildGradleReader, map[*regexp.Regexp]updateFn{
		regexp.MustCompile(versionCodeRegexPattern): func(line string, lineNum int, match []string) string {
			oldVersionCode := match[1]
			res.FinalVersionCode = oldVersionCode
			updatedLine := ""

			if newVersionCode > 0 {
				res.FinalVersionCode = strconv.Itoa(newVersionCode + versionCodeOffset)
				updatedLine = strings.Replace(line, oldVersionCode, res.FinalVersionCode, -1)
				res.UpdatedVersionCodes++
				log.Printf("updating line (%d): %s -> %s", lineNum, line, updatedLine)
			}

			return updatedLine
		},

		regexp.MustCompile(versionNameRegexPattern): func(line string, lineNum int, match []string) string {
			oldVersionName := match[1]
			res.FinalVersionName = oldVersionName
			res.RealVersionName = oldVersionName
			updatedLine := ""
			log.Printf("oldVersionName: %s, versionNameSuffix: %s, versionNameSep: %s", oldVersionName, versionNameSuffix, versionNameSep)
			if versionNameSuffix != "" {
				unquotedVersionNameSuffix := versionNameSuffix
				unquotedVersionName := oldVersionName
				unquotedversionNameSep := versionNameSep
				if !(strings.HasPrefix(unquotedVersionName, `"`) && strings.HasSuffix(unquotedVersionName, `"`)) {
					unquotedVersionName = strings.TrimPrefix(unquotedVersionName, `"`)
					unquotedVersionName = strings.TrimSuffix(unquotedVersionName, `"`)
				}

				if !(strings.HasPrefix(unquotedversionNameSep, `"`) && strings.HasSuffix(unquotedversionNameSep, `"`)) {
					unquotedversionNameSep = strings.TrimPrefix(unquotedversionNameSep, `"`)
					unquotedversionNameSep = strings.TrimSuffix(unquotedversionNameSep, `"`)
				}

				if !(strings.HasPrefix(unquotedVersionNameSuffix, `"`) && strings.HasSuffix(unquotedVersionNameSuffix, `"`)) {
					unquotedVersionNameSuffix = strings.TrimPrefix(unquotedVersionNameSuffix, `"`)
					unquotedVersionNameSuffix = strings.TrimSuffix(unquotedVersionNameSuffix, `"`)
				}

				log.Printf("Processed quotations - unquotedVersionNameSuffix: %s, unquotedVersionName: %s, unquotedversionNameSep: %s", unquotedVersionNameSuffix, unquotedVersionName, unquotedversionNameSep)

				quotedVersionName := ""
				if unquotedversionNameSep != "" {
					quotedVersionName = `"` + unquotedVersionName + unquotedversionNameSep + unquotedVersionNameSuffix + `"`
					log.Printf("final version name - %s", quotedVersionName)
				} else {
					quotedVersionName = `"` + unquotedVersionName + "-" + unquotedVersionNameSuffix + `"`
					log.Printf("final version name with default seperator - %s", quotedVersionName)
				}

				res.FinalVersionName = quotedVersionName
				res.RealVersionName = oldVersionName
				updatedLine = strings.Replace(line, oldVersionName, res.FinalVersionName, -1)
				res.UpdatedVersionNames++
				log.Printf("updating line (%d): %s -> %s", lineNum, line, updatedLine)
			}

			return updatedLine
		},
	})
	if err != nil {
		return UpdateResult{}, err
	}
	return res, nil
}

func main() {
	var cfg config
	if err := stepconf.Parse(&cfg); err != nil {
		failf("Issue with input: %s", err)
	}
	stepconf.Print(cfg)
	fmt.Println()

	if cfg.VersionNameSuffix == "" && cfg.NewVersionCode == 0 {
		failf("Neither NewVersionCode nor VersionNameSuffix are provided, however one of them is required.")
	}

	//
	// find versionName & versionCode with regexp
	fmt.Println()
	log.Infof("Updating versionName and versionCode in: %s", cfg.BuildGradlePth)

	f, err := os.Open(cfg.BuildGradlePth)
	if err != nil {
		failf("Failed to read build.gradle file, error: %s", err)
	}

	versionUpdater := NewBuildGradleVersionUpdater(f)
	res, err := versionUpdater.UpdateVersion(cfg.NewVersionCode, cfg.VersionCodeOffset, cfg.VersionNameSep, cfg.VersionNameSuffix)
	if err != nil {
		failf("Failed to update versions: %s", err)
	}

	//
	// export outputs
	if err := exportOutputs(map[string]string{
		"ANDROID_VERSION_NAME":       res.RealVersionName,
		"ANDROID_FINAL_VERSION_NAME": res.FinalVersionName,
		"ANDROID_VERSION_CODE":       res.FinalVersionCode,
	}); err != nil {
		failf("Failed to export outputs, error: %s", err)
	}

	if err := fileutil.WriteStringToFile(cfg.BuildGradlePth, res.NewContent); err != nil {
		failf("Failed to write build.gradle file, error: %s", err)
	}

	fmt.Println()
	log.Donef("%d versionCode updated", res.UpdatedVersionCodes)
	log.Donef("%d versionName updated", res.UpdatedVersionNames)
}
