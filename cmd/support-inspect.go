// Copyright (c) 2015-2022 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/minio/cli"
	json "github.com/minio/colorjson"
	"github.com/trinet2005/oss-admin-go"
	"github.com/trinet2005/oss-mc/pkg/probe"
	"github.com/trinet2005/oss-pkg/console"
)

const (
	defaultPublicKey      = "MIIBCgKCAQEAs/128UFS9A8YSJY1XqYKt06dLVQQCGDee69T+0Tip/1jGAB4z0/3QMpH0MiS8Wjs4BRWV51qvkfAHzwwdU7y6jxU05ctb/H/WzRj3FYdhhHKdzear9TLJftlTs+xwj2XaADjbLXCV1jGLS889A7f7z5DgABlVZMQd9BjVAR8ED3xRJ2/ZCNuQVJ+A8r7TYPGMY3wWvhhPgPk3Lx4WDZxDiDNlFs4GQSaESSsiVTb9vyGe/94CsCTM6Cw9QG6ifHKCa/rFszPYdKCabAfHcS3eTr0GM+TThSsxO7KfuscbmLJkfQev1srfL2Ii2RbnysqIJVWKEwdW05ID8ryPkuTuwIDAQAB"
	inspectOutputFilename = "inspect-data.enc"
)

var supportInspectFlags = append(subnetCommonFlags,
	cli.BoolFlag{
		Name:  "legacy",
		Usage: "use the older inspect format",
	},
)

var supportInspectCmd = cli.Command{
	Name:            "inspect",
	Usage:           "upload raw object contents for analysis",
	Action:          mainSupportInspect,
	OnUsageError:    onUsageError,
	Before:          setGlobalsFromContext,
	Flags:           supportInspectFlags,
	HideHelpCommand: true,
	CustomHelpTemplate: `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} [FLAGS] TARGET

FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}
EXAMPLES:
  1. Upload 'xl.meta' of a specific object from all the drives
     {{.Prompt}} {{.HelpName}} myminio/bucket/test*/xl.meta

  2. Upload recursively all objects at a prefix. NOTE: This can be an expensive operation use it with caution.
     {{.Prompt}} {{.HelpName}} myminio/bucket/test/**

  3. Download 'xl.meta' of a specific object from all the drives locally, and upload to SUBNET manually
     {{.Prompt}} {{.HelpName}} myminio/bucket/test*/xl.meta --airgap
`,
}

type inspectMessage struct {
	File string `json:"file"`
	Key  string `json:"key,omitempty"`
}

// Colorized message for console printing.
func (t inspectMessage) String() string {
	msg := ""
	if t.Key == "" {
		msg += fmt.Sprintf("File data successfully downloaded as %s\n", console.Colorize("File", t.File))
	} else {
		msg += fmt.Sprintf("Encrypted file data successfully downloaded as %s\n", console.Colorize("File", t.File))
		msg += fmt.Sprintf("Decryption key: %s\n\n", console.Colorize("Key", t.Key))

		msg += "The decryption key will ONLY be shown here. It cannot be recovered.\n"
		msg += "The encrypted file can safely be shared without the decryption key.\n"
		msg += "Even with the decryption key, data stored with encryption cannot be accessed.\n"
	}
	return msg
}

func (t inspectMessage) JSON() string {
	jsonMessageBytes, e := json.MarshalIndent(t, "", " ")
	fatalIf(probe.NewError(e), "Unable to marshal into JSON.")
	return string(jsonMessageBytes)
}

func checkSupportInspectSyntax(ctx *cli.Context) {
	if len(ctx.Args()) != 1 {
		showCommandHelpAndExit(ctx, 1) // last argument is exit code
	}
}

// mainSupportInspect - the entry function of inspect command
func mainSupportInspect(ctx *cli.Context) error {
	// Check for command syntax
	checkSupportInspectSyntax(ctx)

	// Get the alias parameter from cli
	args := ctx.Args()
	aliasedURL := args.Get(0)

	alias, apiKey := initSubnetConnectivity(ctx, aliasedURL, true)
	if len(apiKey) == 0 {
		// api key not passed as flag. Check that the cluster is registered.
		apiKey = validateClusterRegistered(alias, true)
	}

	console.SetColor("File", color.New(color.FgWhite, color.Bold))
	console.SetColor("Key", color.New(color.FgHiRed, color.Bold))

	// Create a new MinIO Admin Client
	client, err := newAdminClient(aliasedURL)
	if err != nil {
		fatalIf(err.Trace(aliasedURL), "Unable to initialize admin client.")
		return nil
	}

	// Compute bucket and object from the aliased URL
	aliasedURL = filepath.ToSlash(aliasedURL)
	splits := splitStr(aliasedURL, "/", 3)
	bucket, prefix := splits[1], splits[2]

	shellName, _ := getShellName()
	if runtime.GOOS != "windows" && shellName != "bash" && strings.Contains(prefix, "*") {
		console.Infoln("Your shell is auto determined as '" + shellName + "', wildcard patterns are only supported with 'bash' SHELL.")
	}

	var publicKey []byte
	if !ctx.Bool("legacy") {
		var e error
		publicKey, e = os.ReadFile(filepath.Join(mustGetMcConfigDir(), "support_public.pem"))
		if e != nil && !os.IsNotExist(e) {
			fatalIf(probe.NewError(e).Trace(aliasedURL), "Unable to inspect file.")
		} else if len(publicKey) > 0 {
			if !globalJSON && !globalQuiet {
				console.Infoln("Using public key from ", filepath.Join(mustGetMcConfigDir(), "support_public.pem"))
			}
		}

		// Fall back to MinIO public key.
		if len(publicKey) == 0 {
			// Public key for MinIO confidential information.
			publicKey, _ = base64.StdEncoding.DecodeString(defaultPublicKey)
		}
	}

	key, r, e := client.Inspect(context.Background(), madmin.InspectOptions{
		Volume:    bucket,
		File:      prefix,
		PublicKey: publicKey,
	})
	fatalIf(probe.NewError(e).Trace(aliasedURL), "Unable to inspect file.")

	// Download the inspect data in a temporary file first
	tmpFile, e := os.CreateTemp("", "mc-inspect-")
	fatalIf(probe.NewError(e), "Unable to download file data.")
	_, e = io.Copy(tmpFile, r)
	fatalIf(probe.NewError(e), "Unable to download file data.")
	r.Close()
	tmpFile.Close()

	if globalAirgapped {
		saveInspectDataFile(key, tmpFile)
		return nil
	}

	uploadURL := subnetUploadURL("inspect", inspectOutputFilename)
	reqURL, headers := prepareSubnetUploadURL(uploadURL, alias, apiKey)

	_, e = uploadFileToSubnet(alias, tmpFile.Name(), reqURL, headers)
	if e != nil {
		console.Errorln("Unable to upload inspect data to SUBNET portal: " + e.Error())
		saveInspectDataFile(key, tmpFile)
		return nil
	}

	console.Infof("Object inspection data for '%s' uploaded to SUBNET successfully\n", aliasedURL)
	return nil
}

func saveInspectDataFile(key []byte, tmpFile *os.File) {
	var keyHex string

	downloadPath := inspectOutputFilename
	// Choose a name and move the inspect data to its final destination
	if key != nil {
		// Create an id that is also crc.
		var id [4]byte
		binary.LittleEndian.PutUint32(id[:], crc32.ChecksumIEEE(key[:]))
		// We use 4 bytes of the 32 bytes to identify they file.
		downloadPath = fmt.Sprintf("inspect-data.%s.enc", hex.EncodeToString(id[:]))
		keyHex = hex.EncodeToString(id[:]) + hex.EncodeToString(key[:])
	}

	fi, e := os.Stat(downloadPath)
	if e == nil && !fi.IsDir() {
		e = moveFile(downloadPath, downloadPath+"."+time.Now().Format(dateTimeFormatFilename))
		fatalIf(probe.NewError(e), "Unable to create a backup of "+downloadPath)
	} else {
		if !os.IsNotExist(e) {
			fatal(probe.NewError(e), "Unable to download file data")
		}
	}

	fatalIf(probe.NewError(moveFile(tmpFile.Name(), downloadPath)), "Unable to rename downloaded data, file exists at %s", tmpFile.Name())

	printMsg(inspectMessage{
		File: downloadPath,
		Key:  keyHex,
	})
}
