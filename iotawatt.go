package iotawatt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/glasslabs/looking-glass/module/types"
)

const (
	apiQueryPath = "query"
)

// Config is the module configuration.
type Config struct {
	URL    string   `yaml:"url"`
	Inputs []string `yaml:"inputs"`

	Interval time.Duration `yaml:"interval"`
}

// NewConfig creates a default configuration for the module.
func NewConfig() *Config {
	return &Config{
		Interval: 2 * time.Second,
	}
}

// Module is a clock module.
type Module struct {
	name string
	path string
	cfg  *Config
	ui   types.UI
	log  types.Logger

	baseURL *url.URL
	qryVals url.Values

	done chan struct{}
}

// New returns a running clock module.
func New(_ context.Context, cfg *Config, info types.Info, ui types.UI) (io.Closer, error) {
	qryValues := url.Values{
		"format":     []string{"json"},
		"resolution": []string{"high"},
		"missing":    []string{"null"},
		"begin":      []string{"s-1h"},
		"end":        []string{"s"},
		"group":      []string{"auto"},
	}
	inputs := append([]string{"time.utc.unix"}, cfg.Inputs...)
	qryValues.Set("select", "["+strings.Join(inputs, ",")+"]")

	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("could not parse url: %w", err)
	}

	m := &Module{
		name:    info.Name,
		path:    info.Path,
		cfg:     cfg,
		ui:      ui,
		log:     info.Log,
		baseURL: u,
		qryVals: qryValues,
		done:    make(chan struct{}),
	}

	if err = m.loadCSS("assets/style.css"); err != nil {
		return nil, err
	}
	if err = m.renderHTML("assets/index.html"); err != nil {
		return nil, err
	}

	go m.run()

	return m, nil
}

type series struct {
	Data [][]float64 `json:"data"`
}

func (m *Module) run() {
	c := http.Client{}

	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()

	for {
		var raw [][]float64
		if err := m.request(c, &raw); err != nil {
			m.log.Error("Could not get current IoTaWatt data", "module", "iotawatt", "id", m.name, "error", err.Error())
			return
		}

		l := len(m.cfg.Inputs)
		var current float64
		series := make([]series, l)
		for _, row := range raw {
			var curr float64
			for i := 1; i <= l; i++ {
				kw := row[i] / 1000
				series[i-1].Data = append(series[i-1].Data, []float64{row[0], kw})
				curr += kw
			}
			current = curr
		}

		b, err := json.Marshal(series)
		if err != nil {
			m.log.Error("could not encode data", "module", "iotawatt", "id", m.name, "error", err.Error())
			return
		}

		f := strconv.FormatFloat(current, 'f', 1, 64)
		_, err = m.ui.Eval("document.querySelector('#%s .current .number').innerText = '%s'", m.name, f)
		if err != nil {
			m.log.Error("Could not update current", "module", "iotawatt", "id", m.name, "error", err.Error())
		}
		if _, err = m.ui.Eval("iotaWattSeries = %s", string(b)); err != nil {
			m.log.Error("Could not update series", "module", "iotawatt", "id", m.name, "error", err.Error())
		}
		if _, err = m.ui.Eval("iotaWattChart.update({series: iotaWattSeries})"); err != nil {
			m.log.Error("Could not update chart", "module", "iotawatt", "id", m.name, "error", err.Error())
		}

		select {
		case <-m.done:
			return
		case <-ticker.C:
			continue
		}
	}
}

func (m *Module) loadCSS(path string) error {
	css, err := os.ReadFile(filepath.Clean(filepath.Join(m.path, path)))
	if err != nil {
		return fmt.Errorf("iotawatt: could not read css: %w", err)
	}
	return m.ui.LoadCSS(string(css))
}

func (m *Module) renderHTML(path string) error {
	html, err := os.ReadFile(filepath.Clean(filepath.Join(m.path, path)))
	if err != nil {
		return fmt.Errorf("iotawatt: could not read html: %w", err)
	}
	if err = m.ui.LoadHTML(string(html)); err != nil {
		return fmt.Errorf("iotawatt: could not load html: %w", err)
	}

	_, err = m.ui.Eval("invokeModuleScripts(%q)", m.name)
	return err
}

func (m *Module) request(c http.Client, v interface{}) error {
	u, err := m.baseURL.Parse(apiQueryPath)
	if err != nil {
		return fmt.Errorf("could not parse url: %w", err)
	}
	u.RawQuery = m.qryVals.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("could create request: %w", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("could not parse url: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return errors.New("expected status code")
	}

	if err = json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("could not parse data: %w", err)
	}
	return nil
}

// Close stops and closes the module.
func (m *Module) Close() error {
	close(m.done)
	return nil
}
