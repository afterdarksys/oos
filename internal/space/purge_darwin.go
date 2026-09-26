//go:build darwin

package space

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const purgeTimeout = 15 * time.Second

// purgeScript prints available bytes and important-usage bytes. The volume
// comes from the environment so a path cannot change the script.
const purgeScript = `
ObjC.import("Foundation");
var env = $.NSProcessInfo.processInfo.environment;
var path = env.objectForKey("OOS_VOLUME").js;
if (!path) { throw new Error("no volume"); }
var u = $.NSURL.fileURLWithPath(path);
var err = Ref();
var keys = $.NSArray.arrayWithArray(["NSURLVolumeAvailableCapacityKey", "NSURLVolumeAvailableCapacityForImportantUsageKey"]);
var v = u.resourceValuesForKeysError(keys, err);
if (!v || v.isNil()) { throw new Error("no capacity"); }
function num(k) {
  var o = v.objectForKey(k);
  if (!o || o.isNil()) { return -1; }
  return o.longLongValue;
}
num("NSURLVolumeAvailableCapacityKey") + "\n" + num("NSURLVolumeAvailableCapacityForImportantUsageKey");
`

// Purgeable asks CacheDelete, through Foundation, how many bytes macOS
// believes it could reclaim. The result is an estimate.
func Purgeable(volume string) (int64, error) {
	if volume == "" {
		volume = "/"
	}
	if !filepath.IsAbs(volume) || strings.ContainsAny(volume, "\n\r\x00") {
		return 0, fmt.Errorf("bad volume %q", volume)
	}
	ctx, cancel := context.WithTimeout(context.Background(), purgeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", purgeScript)
	cmd.Env = append(os.Environ(), "OOS_VOLUME="+volume)
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return 0, fmt.Errorf("osascript timed out after %s", purgeTimeout)
	}
	if err != nil {
		return 0, fmt.Errorf("osascript: %v", err)
	}
	return ParsePurgeable(string(out))
}
