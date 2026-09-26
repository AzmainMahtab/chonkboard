package components

import (
	"strings"
	"unicode"

	"github.com/AzmainMahtab/chonkboard/web/view"
)

// initials renders an avatar fallback from a display name.
func initials(name string) string {
	var out []rune
	for _, part := range strings.Fields(name) {
		for _, r := range part {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, unicode.ToUpper(r))
				break
			}
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

// labelTint and laneAccent map a stored token name to literal Tailwind classes.
// The class strings must appear verbatim in a scanned file, which is why these
// are switches over literals rather than fmt.Sprintf onto a prefix.
func labelTint(color string) string {
	switch color {
	case "blue":
		return "bg-lane-blue/12 text-lane-blue"
	case "teal":
		return "bg-lane-teal/12 text-lane-teal"
	case "green":
		return "bg-lane-green/12 text-lane-green"
	case "amber":
		return "bg-lane-amber/12 text-lane-amber"
	case "rose":
		return "bg-lane-rose/12 text-lane-rose"
	case "violet":
		return "bg-lane-violet/12 text-lane-violet"
	default:
		return "bg-lane-slate/12 text-lane-slate"
	}
}

func laneAccent(color string) string {
	switch color {
	case "blue":
		return "bg-lane-blue"
	case "teal":
		return "bg-lane-teal"
	case "green":
		return "bg-lane-green"
	case "amber":
		return "bg-lane-amber"
	case "rose":
		return "bg-lane-rose"
	case "violet":
		return "bg-lane-violet"
	default:
		return "bg-lane-slate"
	}
}

func countTint(l view.Lane) string {
	if l.OverWIP() {
		return "bg-danger/15 text-danger font-semibold"
	}
	return "text-ink-3"
}

func toastTint(tone string) string {
	switch tone {
	case "error":
		return "border-danger/40 bg-surface text-danger"
	case "ok":
		return "border-ok/40 bg-surface text-ok"
	default:
		return "border-line bg-surface text-ink-2"
	}
}

func toastRole(tone string) string {
	if tone == "error" {
		return "alert"
	}
	return "status"
}
