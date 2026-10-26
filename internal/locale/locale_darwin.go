package locale

import (
	"os/exec"
	"strings"
)

func preferred() string {
	// AppleLanguages is a property list array like ( "tr-TR", "en-US" ).
	out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.Trim(strings.TrimSpace(line), `",()`)
			if line != "" {
				return line
			}
		}
	}
	out, err = exec.Command("defaults", "read", "-g", "AppleLocale").Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}
