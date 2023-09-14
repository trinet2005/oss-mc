// Copyright (c) 2015-2023 MinIO, Inc.
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
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/dustin/go-humanize/english"
	"github.com/fatih/color"
	"github.com/minio/cli"
	json "github.com/minio/colorjson"
	"github.com/trinet2005/oss-admin-go"
	"github.com/trinet2005/oss-go-sdk/pkg/set"
	"github.com/trinet2005/oss-mc/pkg/probe"
	"github.com/trinet2005/oss-pkg/console"
)

var adminInfoCmd = cli.Command{
	Name:         "info",
	Usage:        "display MinIO server information",
	Action:       mainAdminInfo,
	OnUsageError: onUsageError,
	Before:       setGlobalsFromContext,
	Flags:        globalFlags,
	CustomHelpTemplate: `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} TARGET

FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}
EXAMPLES:
  1. Get server information of the 'play' MinIO server.
     {{.Prompt}} {{.HelpName}} play/
`,
}

type poolSummary struct {
	index          int
	setsCount      int
	drivesPerSet   int
	driveTolerance int
	endpoints      set.StringSet
}

type clusterInfo map[int]*poolSummary

func clusterSummaryInfo(info madmin.InfoMessage) clusterInfo {
	summary := make(clusterInfo)

	for _, srv := range info.Servers {
		for _, disk := range srv.Disks {
			pool := summary[disk.PoolIndex]
			if pool == nil {
				pool = &poolSummary{
					index:          disk.PoolIndex,
					endpoints:      set.NewStringSet(),
					driveTolerance: info.StandardParity(),
				}
			}
			// Deprecated calculation based on disk location
			if disk.SetIndex+1 > pool.setsCount {
				pool.setsCount = disk.SetIndex + 1
			}
			// Deprecated calculation based on disk location
			if disk.DiskIndex+1 > pool.drivesPerSet {
				pool.drivesPerSet = disk.DiskIndex + 1
			}
			pool.endpoints.Add(srv.Endpoint)
			summary[disk.PoolIndex] = pool
		}
	}

	if len(info.Backend.TotalSets) > 0 { // Check if this is a recent enough MinIO version
		for _, pool := range summary {
			pool.setsCount = info.Backend.TotalSets[pool.index]
			pool.drivesPerSet = info.Backend.DrivesPerSet[pool.index]
		}
	}
	return summary
}

func endpointToPools(endpoint string, c clusterInfo) (pools []int) {
	for poolNumber, poolSummary := range c {
		if poolSummary.endpoints.Contains(endpoint) {
			pools = append(pools, poolNumber)
		}
	}
	sort.Ints(pools)
	return
}

// Wrap "Info" message together with fields "Status" and "Error"
type clusterStruct struct {
	Status string             `json:"status"`
	Error  string             `json:"error,omitempty"`
	Info   madmin.InfoMessage `json:"info,omitempty"`
}

// String provides colorized info messages depending on the type of a server
//
//	FS server                          non-FS server
//
// ==============================  ===================================
// ● <ip>:<port>                   ● <ip>:<port>
//
//	Uptime: xxx                     Uptime: xxx
//	Version: xxx                    Version: xxx
//	Network: X/Y OK                 Network: X/Y OK
//
// U Used, B Buckets, O Objects    Drives: N/N OK
//
//	U Used, B Buckets, O Objects
//	N drives online, K drives offline
func (u clusterStruct) String() (msg string) {
	// Check cluster level "Status" field for error
	if u.Status == "error" {
		fatal(probe.NewError(errors.New(u.Error)), "Unable to get service status")
	}

	// If nothing has been collected, error out
	if u.Info.Servers == nil {
		fatal(probe.NewError(errors.New("Unable to get service status")), "")
	}

	// Initialization
	var totalOfflineNodes int

	// Color palette initialization
	console.SetColor("Info", color.New(color.FgGreen, color.Bold))
	console.SetColor("InfoFail", color.New(color.FgRed, color.Bold))
	console.SetColor("InfoWarning", color.New(color.FgYellow, color.Bold))

	backendType := u.Info.BackendType()

	coloredDot := console.Colorize("Info", dot)
	if madmin.ItemState(u.Info.Mode) == madmin.ItemInitializing {
		coloredDot = console.Colorize("InfoWarning", dot)
	}

	sort.Slice(u.Info.Servers, func(i, j int) bool {
		return u.Info.Servers[i].Endpoint < u.Info.Servers[j].Endpoint
	})

	clusterSummary := clusterSummaryInfo(u.Info)

	// Loop through each server and put together info for each one
	for _, srv := range u.Info.Servers {
		// Check if MinIO server is not online ("Mode" field),
		if srv.State != string(madmin.ItemOnline) {
			totalOfflineNodes++
			// "PrintB" is color blue in console library package
			msg += fmt.Sprintf("%s  %s\n", console.Colorize("InfoFail", dot), console.Colorize("PrintB", srv.Endpoint))
			msg += fmt.Sprintf("   Uptime: %s\n", console.Colorize("InfoFail", srv.State))

			if backendType == madmin.Erasure {
				// Info about drives on a server, only available for non-FS types
				var OffDrives int
				var OnDrives int
				var dispNoOfDrives string
				for _, disk := range srv.Disks {
					switch disk.State {
					case madmin.DriveStateOk, madmin.DriveStateUnformatted:
						OnDrives++
					default:
						OffDrives++
					}
				}

				totalDrivesPerServer := OnDrives + OffDrives

				dispNoOfDrives = strconv.Itoa(OnDrives) + "/" + strconv.Itoa(totalDrivesPerServer)
				msg += fmt.Sprintf("   Drives: %s %s\n", dispNoOfDrives, console.Colorize("InfoFail", "OK "))
			}

			msg += "\n"

			// Continue to the next server
			continue
		}

		// Print server title
		msg += fmt.Sprintf("%s  %s\n", coloredDot, console.Colorize("PrintB", srv.Endpoint))

		// Uptime
		msg += fmt.Sprintf("   Uptime: %s\n", console.Colorize("Info",
			humanize.RelTime(time.Now(), time.Now().Add(time.Duration(srv.Uptime)*time.Second), "", "")))

		// Version
		version := srv.Version
		if srv.Version == "DEVELOPMENT.GOGET" {
			version = "<development>"
		}
		msg += fmt.Sprintf("   Version: %s\n", version)
		// Network info, only available for non-FS types
		connectionAlive := 0
		totalNodes := len(srv.Network)
		if srv.Network != nil && backendType == madmin.Erasure {
			for _, v := range srv.Network {
				if v == "online" {
					connectionAlive++
				}
			}
			clr := "Info"
			if connectionAlive != totalNodes {
				clr = "InfoWarning"
			}
			displayNwInfo := strconv.Itoa(connectionAlive) + "/" + strconv.Itoa(totalNodes)
			msg += fmt.Sprintf("   Network: %s %s\n", displayNwInfo, console.Colorize(clr, "OK "))
		}

		if backendType == madmin.Erasure {
			// Info about drives on a server, only available for non-FS types
			var OffDrives int
			var OnDrives int
			var dispNoOfDrives string
			for _, disk := range srv.Disks {
				switch disk.State {
				case madmin.DriveStateOk, madmin.DriveStateUnformatted:
					OnDrives++
				default:
					OffDrives++
				}
			}

			totalDrivesPerServer := OnDrives + OffDrives
			clr := "Info"
			if OnDrives != totalDrivesPerServer {
				clr = "InfoWarning"
			}
			dispNoOfDrives = strconv.Itoa(OnDrives) + "/" + strconv.Itoa(totalDrivesPerServer)
			msg += fmt.Sprintf("   Drives: %s %s\n", dispNoOfDrives, console.Colorize(clr, "OK "))

			// Print pools belonging to this server
			var prettyPools []string
			for _, pool := range endpointToPools(srv.Endpoint, clusterSummary) {
				prettyPools = append(prettyPools, strconv.Itoa(pool+1))
			}
			msg += fmt.Sprintf("   Pool: %s\n", console.Colorize("Info", fmt.Sprintf("%+v", strings.Join(prettyPools, ", "))))
		}

		msg += "\n"
	}

	if backendType == madmin.Erasure {
		msg += "Pools:\n"
		for pool, summary := range clusterSummary {
			msg += fmt.Sprintf("   %s, Erasure sets: %d, Drives per erasure set: %d\n",
				console.Colorize("Info", humanize.Ordinal(pool+1)), summary.setsCount, summary.drivesPerSet)
		}
	}

	msg += "\n"

	// Summary on used space, total no of buckets and
	// total no of objects at the Cluster level
	usedTotal := humanize.IBytes(u.Info.Usage.Size)
	if u.Info.Buckets.Count > 0 {
		msg += fmt.Sprintf("%s Used, %s, %s", usedTotal,
			english.Plural(int(u.Info.Buckets.Count), "Bucket", ""),
			english.Plural(int(u.Info.Objects.Count), "Object", ""))
		if u.Info.Versions.Count > 0 {
			msg += ", " + english.Plural(int(u.Info.Versions.Count), "Version", "")
		}
		if u.Info.DeleteMarkers.Count > 0 {
			msg += ", " + english.Plural(int(u.Info.DeleteMarkers.Count), "Delete Marker", "")
		}
		msg += "\n"
	}
	if backendType == madmin.Erasure {
		if totalOfflineNodes != 0 {
			msg += fmt.Sprintf("%s offline, ", english.Plural(totalOfflineNodes, "node", ""))
		}
		// Summary on total no of online and total
		// number of offline drives at the Cluster level
		msg += fmt.Sprintf("%s online, %s offline\n",
			english.Plural(u.Info.Backend.OnlineDisks, "drive", ""),
			english.Plural(u.Info.Backend.OfflineDisks, "drive", ""))
	}

	// Remove the last new line if any
	// since this is a String() function
	msg = strings.TrimSuffix(msg, "\n")
	return
}

// JSON jsonifies service status message.
func (u clusterStruct) JSON() string {
	statusJSONBytes, e := json.MarshalIndent(u, "", "    ")
	fatalIf(probe.NewError(e), "Unable to marshal into JSON.")

	return string(statusJSONBytes)
}

// checkAdminInfoSyntax - validate arguments passed by a user
func checkAdminInfoSyntax(ctx *cli.Context) {
	if len(ctx.Args()) == 0 || len(ctx.Args()) > 1 {
		showCommandHelpAndExit(ctx, 1) // last argument is exit code
	}
}

func mainAdminInfo(ctx *cli.Context) error {
	checkAdminInfoSyntax(ctx)

	// Get the alias parameter from cli
	args := ctx.Args()
	aliasedURL := args.Get(0)

	// Create a new MinIO Admin Client
	client, err := newAdminClient(aliasedURL)
	fatalIf(err, "Unable to initialize admin connection.")

	var clusterInfo clusterStruct
	// Fetch info of all servers (cluster or single server)
	admInfo, e := client.ServerInfo(globalContext)
	if e != nil {
		clusterInfo.Status = "error"
		clusterInfo.Error = e.Error()
	} else {
		clusterInfo.Status = "success"
		clusterInfo.Error = ""
	}
	clusterInfo.Info = admInfo
	printMsg(clusterInfo)

	return nil
}
