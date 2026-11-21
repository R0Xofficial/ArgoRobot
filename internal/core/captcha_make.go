package core

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"strconv"
	"strings"
)

// The tests of a captcha (core/captchas.py: make and picture).

// Captcha modes.
var CaptchaModes = []string{"button", "math", "text", "text2"}

const (
	captchaCodeLength = 5
	captchaAlphabet   = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // nothing that looks like something else
)

// CaptchaTest is a new test: its question, the right answer and the order of the buttons.
type CaptchaTest struct{ Question, Answer, Layout string }

// MakeCaptcha makes a test of the mode. A nil rng uses the default source.
func MakeCaptcha(mode string, rng *rand.Rand) CaptchaTest {
	intn := func(lo, hi int) int { // randint(lo, hi), both included
		if rng == nil {
			return lo + rand.Intn(hi-lo+1)
		}
		return lo + rng.Intn(hi-lo+1)
	}
	shuffle := func(n int, swap func(i, j int)) {
		if rng == nil {
			rand.Shuffle(n, swap)
		} else {
			rng.Shuffle(n, swap)
		}
	}
	switch mode {
	case "math":
		kind := "+-x"[intn(0, 2)]
		var a, b, result int
		switch kind {
		case '+':
			a, b = intn(2, 20), intn(2, 20)
			result = a + b
		case '-':
			a = intn(10, 30)
			b = intn(2, a-1)
			result = a - b
		default:
			a, b = intn(2, 9), intn(2, 9)
			result = a * b
		}
		options := map[int]bool{result: true}
		var layout []int
		layout = append(layout, result)
		for len(options) < 6 {
			sign := []int{-1, 1}[intn(0, 1)]
			if wrong := result + sign*intn(1, 10); wrong >= 0 && !options[wrong] {
				options[wrong] = true
				layout = append(layout, wrong)
			}
		}
		shuffle(len(layout), func(i, j int) { layout[i], layout[j] = layout[j], layout[i] })
		parts := make([]string, len(layout))
		for i, o := range layout {
			parts[i] = strconv.Itoa(o)
		}
		symbol := string(kind)
		if kind == 'x' {
			symbol = "×"
		}
		return CaptchaTest{Question: strconv.Itoa(a) + " " + symbol + " " + strconv.Itoa(b) + " = ?", Answer: strconv.Itoa(result), Layout: strings.Join(parts, ",")}
	case "text", "text2":
		pool := []byte(captchaAlphabet)
		shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		code := string(pool[:captchaCodeLength])
		letters := []byte(code)
		for {
			shuffle(len(letters), func(i, j int) { letters[i], letters[j] = letters[j], letters[i] })
			if string(letters) != code {
				break
			}
		}
		return CaptchaTest{Answer: code, Layout: string(letters)}
	}
	return CaptchaTest{}
}

// glyphs are the characters of the alphabet as 5x7 dots, drawn big and tilted into the picture.
var glyphs = map[byte][7]string{
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'B': {"11110", "10001", "10001", "11110", "10001", "10001", "11110"},
	'C': {"01110", "10001", "10000", "10000", "10000", "10001", "01110"},
	'D': {"11110", "10001", "10001", "10001", "10001", "10001", "11110"},
	'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"},
	'F': {"11111", "10000", "10000", "11110", "10000", "10000", "10000"},
	'G': {"01110", "10001", "10000", "10111", "10001", "10001", "01111"},
	'H': {"10001", "10001", "10001", "11111", "10001", "10001", "10001"},
	'J': {"00111", "00010", "00010", "00010", "00010", "10010", "01100"},
	'K': {"10001", "10010", "10100", "11000", "10100", "10010", "10001"},
	'L': {"10000", "10000", "10000", "10000", "10000", "10000", "11111"},
	'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'N': {"10001", "11001", "10101", "10011", "10001", "10001", "10001"},
	'P': {"11110", "10001", "10001", "11110", "10000", "10000", "10000"},
	'Q': {"01110", "10001", "10001", "10001", "10101", "10010", "01101"},
	'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"},
	'S': {"01111", "10000", "10000", "01110", "00001", "00001", "11110"},
	'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"},
	'U': {"10001", "10001", "10001", "10001", "10001", "10001", "01110"},
	'V': {"10001", "10001", "10001", "10001", "10001", "01010", "00100"},
	'W': {"10001", "10001", "10001", "10101", "10101", "11011", "10001"},
	'X': {"10001", "10001", "01010", "00100", "01010", "10001", "10001"},
	'Y': {"10001", "10001", "01010", "00100", "00100", "00100", "00100"},
	'Z': {"11111", "00001", "00010", "00100", "01000", "10000", "11111"},
	'2': {"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	'3': {"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	'4': {"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	'5': {"11111", "10000", "11110", "00001", "00001", "10001", "01110"},
	'6': {"00110", "01000", "10000", "11110", "10001", "10001", "01110"},
	'7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	'9': {"01110", "10001", "10001", "01111", "00001", "00010", "01100"},
}

// glyphCoverage is how much of the dot-matrix character covers the point (u, v) of its box (0..1 each).
func glyphCoverage(rows [7]string, u, v float64) bool {
	if u < 0 || u >= 1 || v < 0 || v >= 1 {
		return false
	}
	x, y := int(u*5), int(v*7)
	// Rounded-off dots: only the middle of each dot is ink, so the letters read as one stroke when large.
	return rows[y][x] == '1'
}

// CaptchaPicture draws the code as a PNG: tilted letters on a noisy background. A nil rng uses the default source.
func CaptchaPicture(code string, rng *rand.Rand) []byte {
	r := rng
	if r == nil {
		r = rand.New(rand.NewSource(rand.Int63()))
	}
	between := func(lo, hi int) int { return lo + r.Intn(hi-lo+1) }
	const size = 46
	width, height := 40+len(code)*(size+8), size+70
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	bg := uint8(between(225, 245))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{bg, bg, bg, 255})
		}
	}
	line := func(x0, y0, x1, y1 int, c color.RGBA, w int) {
		steps := int(math.Max(math.Abs(float64(x1-x0)), math.Abs(float64(y1-y0))))
		for i := 0; i <= steps; i++ {
			t := 0.0
			if steps > 0 {
				t = float64(i) / float64(steps)
			}
			px, py := int(float64(x0)+t*float64(x1-x0)), int(float64(y0)+t*float64(y1-y0))
			for dx := 0; dx < w; dx++ {
				for dy := 0; dy < w; dy++ {
					if image.Pt(px+dx, py+dy).In(img.Bounds()) {
						img.Set(px+dx, py+dy, c)
					}
				}
			}
		}
	}
	for i := 0; i < 14; i++ {
		c := color.RGBA{uint8(between(120, 200)), uint8(between(120, 200)), uint8(between(120, 200)), 255}
		line(between(0, width), between(0, height), between(0, width), between(0, height), c, between(1, 3))
	}
	x := 24
	for i := 0; i < len(code); i++ {
		rows, ok := glyphs[code[i]]
		if !ok {
			continue
		}
		ink := color.RGBA{uint8(between(20, 110)), uint8(between(20, 110)), uint8(between(20, 110)), 255}
		angle := float64(between(-22, 22)) * math.Pi / 180
		cos, sin := math.Cos(angle), math.Sin(angle)
		gw, gh := float64(size)*5/7, float64(size) // the box of the letter
		top := between(14, height-size-14)
		cx, cy := float64(x)+gw/2, float64(top)+gh/2
		reach := int(math.Hypot(gw, gh)/2) + 2
		for py := int(cy) - reach; py <= int(cy)+reach; py++ {
			for px := int(cx) - reach; px <= int(cx)+reach; px++ {
				if !image.Pt(px, py).In(img.Bounds()) {
					continue
				}
				// rotate the point back into the letter's own box
				dx, dy := float64(px)-cx, float64(py)-cy
				u := (dx*cos+dy*sin)/gw + 0.5
				v := (-dx*sin+dy*cos)/gh + 0.5
				if glyphCoverage(rows, u, v) {
					img.Set(px, py, ink)
				}
			}
		}
		x += size + 8
	}
	for i := 0; i < 260; i++ {
		g := uint8(between(0, 160))
		img.Set(between(0, width-1), between(0, height-1), color.RGBA{g, g, g, 255})
	}
	for i := 0; i < 5; i++ {
		c := color.RGBA{uint8(between(60, 160)), uint8(between(60, 160)), uint8(between(60, 160)), 255}
		line(0, between(0, height), width, between(0, height), c, 1)
	}
	var out bytes.Buffer
	_ = png.Encode(&out, img)
	return out.Bytes()
}
