// SPDX-License-Identifier: AGPL-3.0-or-later

package newsletter

import (
	"fmt"
	"math"
	"strconv"
)

// Contrast is the WCAG 2 contrast ratio of two "#rrggbb" colors, from 1 to
// 21; 1 when either is not such a color.
func Contrast(a, b string) float64 {
	la, okA := luminance(a)
	lb, okB := luminance(b)
	if !okA || !okB {
		return 1
	}
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(c string) (float64, bool) {
	if !hexColor.MatchString(c) {
		return 0, false
	}
	var rgb [3]float64
	for i := range rgb {
		v, _ := strconv.ParseUint(c[1+2*i:3+2*i], 16, 8)
		s := float64(v) / 255
		if s <= 0.04045 {
			rgb[i] = s / 12.92
		} else {
			rgb[i] = math.Pow((s+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*rgb[0] + 0.7152*rgb[1] + 0.0722*rgb[2], true
}

// DarkCard is the card's background in a mail app's dark mode (the page's
// prefers-color-scheme styles).
const DarkCard = "#1b1f24"

// DarkAccent is accent as links show it in dark mode: the accent itself
// when it reads on the dark card (4.5:1), else the accent mixed with white
// just enough to. An accent chosen to read on white is usually too dark
// for the dark card. Not a "#rrggbb" color, it is DefaultAccent's.
func DarkAccent(accent string) string {
	if !hexColor.MatchString(accent) {
		accent = DefaultAccent
	}
	var rgb [3]float64
	for i := range rgb {
		v, _ := strconv.ParseUint(accent[1+2*i:3+2*i], 16, 8)
		rgb[i] = float64(v)
	}
	for step := 0; step <= 20; step++ {
		t := float64(step) / 20
		c := fmt.Sprintf("#%02x%02x%02x", mix(rgb[0], t), mix(rgb[1], t), mix(rgb[2], t))
		if Contrast(c, DarkCard) >= 4.5 {
			return c
		}
	}
	return "#ffffff"
}

func mix(v, t float64) int { return int(math.Round(v + (255-v)*t)) }
