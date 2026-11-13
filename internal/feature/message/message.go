package message

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	gogui "github.com/go-gui-org/go-gui/gui"
	xdraw "golang.org/x/image/draw"

	shared "github.com/ygelfand/libcountertop/pkg/display/message"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
	"github.com/ygelfand/libcountertop/pkg/settings/schema"

	"github.com/ygelfand/LANovo/internal/component"
	"github.com/ygelfand/LANovo/internal/feature/alerts"
)

var get = sync.OnceValue(func() *Messages {
	m := &Messages{Messages: shared.New()}
	m.Messages.SoundWith(alerts.Get())
	return m
})

func Get() *Messages { return get() }

func init() { component.Register(sharedcomponent.Device, Get, sharedcomponent.Order(40)) }

// Messages wraps shared.Messages and extends the show_message action
// to support picture, style, icon, and keys fields.
type Messages struct {
	*shared.Messages

	mu    sync.Mutex
	gen   uint64
	shown map[string]shared.Picture
}

func (m *Messages) Actions() []*esphome.Action {
	base := m.Messages.Actions()
	extended := make([]*esphome.Action, len(base))
	for i, a := range base {
		if a.Name == "show_message" {
			extended[i] = m.extendedShowMessage(a)
		} else {
			extended[i] = a
		}
	}
	return extended
}

func (m *Messages) extendedShowMessage(base *esphome.Action) *esphome.Action {
	return &esphome.Action{
		Name:        base.Name,
		Description: base.Description,
		Args: append(base.Args,
			esphome.Arg{
				Name:        "picture",
				Type:        esphome.ArgString,
				Description: "URL of an image to show in the message.",
				Example:     "http://ha.local/api/frigate/snapshot/front_door",
			},
			esphome.Arg{
				Name:        "style",
				Type:        esphome.ArgString,
				Description: "Alert style: banner, card, full, or toast.",
				Example:     "card",
			},
			esphome.Arg{
				Name:        "icon",
				Type:        esphome.ArgString,
				Description: "Material Design icon name, e.g. mdi:motion-sensor.",
				Example:     "mdi:motion-sensor",
			},
			esphome.Arg{
				Name:        "keys",
				Type:        esphome.ArgString,
				Description: "Comma-separated button labels, e.g. 'View,Dismiss'.",
				Example:     "View,Dismiss",
			},
		),
		Run: func(c esphome.Call) (any, error) {
			body := c.String("body")
			if body == "" {
				return nil, fmt.Errorf("show_message needs a body")
			}

			msg := shared.Message{
				Title: c.String("title"),
				Body:  body,
				Tone:  tone(c.String("tone")),
			}

			// Parse optional style
			if s := c.String("style"); s != "" {
				msg.Style = parseStyle(s)
			}

			// Parse optional icon
			if s := c.String("icon"); s != "" {
				msg.Icon = s
			}

			// Parse optional keys (comma-separated button labels)
			if s := c.String("keys"); s != "" {
				for _, k := range strings.Split(s, ",") {
					k = strings.TrimSpace(k)
					if k != "" {
						msg.Keys = append(msg.Keys, shared.Key{Text: k})
					}
				}
			}

			// Fetch optional picture from URL
			if url := c.String("picture"); url != "" {
				pic, err := m.fetchPicture(url)
				if err != nil {
					slog.Warn("show_message: fetching picture failed", "url", url, "err", err)
				} else {
					msg.Picture = pic
				}
			}

			m.Show(msg, Hold(c.Int("seconds")))
			return nil, nil
		},
	}
}

func parseStyle(s string) schema.AlertStyle {
	switch strings.ToLower(s) {
	case "banner":
		return schema.AlertBanner
	case "card":
		return schema.AlertCard
	case "full", "fullscreen":
		return schema.AlertFull
	case "toast":
		return schema.AlertToast
	default:
		return schema.AlertCard
	}
}

func (m *Messages) fetchPicture(url string) (shared.Picture, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return shared.Picture{}, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return shared.Picture{}, fmt.Errorf("http status %d", resp.StatusCode)
	}

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return shared.Picture{}, fmt.Errorf("image decode: %w", err)
	}

	return m.keepImage(url, shrink(img, 800)), nil
}

func (m *Messages) keepImage(name string, rgba *image.RGBA) shared.Picture {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shown == nil {
		m.shown = map[string]shared.Picture{}
	}
	m.gen++
	key := fmt.Sprintf("message/%s/%d", name, m.gen)
	pic := shared.Picture{
		Src:    gogui.UseImage(key, rgba.Rect.Dx(), rgba.Rect.Dy(), rgba.Pix),
		Width:  rgba.Rect.Dx(),
		Height: rgba.Rect.Dy(),
	}
	if old, ok := m.shown[name]; ok {
		gogui.DropImage(old.Src)
	}
	m.shown[name] = pic
	return pic
}

func shrink(img image.Image, most int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if most > 0 && max(w, h) > most {
		if w >= h {
			w, h = most, max(1, h*most/w)
		} else {
			w, h = max(1, w*most/h), most
		}
		out := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.ApproxBiLinear.Scale(out, out.Rect, img, b, draw.Src, nil)
		return out
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(out, out.Rect, img, b.Min, draw.Src)
	return out
}

func tone(name string) schema.Tone {
	for _, t := range schema.Tones() {
		if string(t) == name {
			return t
		}
	}
	return schema.ToneInfo
}

func Hold(v int) time.Duration {
	switch {
	case v <= 0:
		v = shared.DefaultSeconds
	case v > shared.MaxSeconds:
		v = shared.MaxSeconds
	}
	return time.Duration(v) * time.Second
}
