package streaming

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"

	_ "github.com/TykTechnologies/benthos/v4/public/components/all"
	"github.com/TykTechnologies/benthos/v4/public/service"

	_ "github.com/TykTechnologies/tyk/internal/portal"
)

type Stream struct {
	allowedUnsafe []string
	streamConfig  string
	stream        *service.Stream
	log           *logrus.Logger
}

func NewStream(allowUnsafe []string) *Stream {
	logger := logrus.New()
	logger.Out = log.Writer()
	logger.Formatter = &logrus.TextFormatter{
		FullTimestamp: true,
	}
	logger.Level = logrus.DebugLevel

	if len(allowUnsafe) > 0 {
		logger.Warnf("Allowing unsafe components: %v", allowUnsafe)
	}

	return &Stream{
		log:           logger,
		allowedUnsafe: allowUnsafe,
	}
}

func (s *Stream) SetLogger(logger *logrus.Logger) {
	if logger != nil {
		s.log = logger
	}
}

func (s *Stream) Start(config map[string]interface{}, mux service.HTTPMultiplexer) error {
	s.log.Debugf("Starting stream")

	configPayload, err := yaml.Marshal(config)
	if err != nil {
		s.log.Errorf("Failed to marshal config: %v", err)
		return err
	}

	configPayload = s.removeUnsafe(configPayload)
	if err != nil {
		s.log.Errorf("Failed to remove consumer_group: %v", err)
		return err
	}

	s.log.Debugf("Building new stream")
	builder := service.NewStreamBuilder()

	err = builder.SetYAML(string(configPayload))
	if err != nil {
		s.log.Errorf("Failed to set YAML: %v", err)
		return err
	}

	if mux != nil {
		builder.SetHTTPMux(mux)
	}

	stream, err := builder.Build()
	if err != nil {
		s.log.Errorf("Failed to build stream: %v", err)
		return err
	}

	s.streamConfig = string(configPayload)
	s.stream = stream

	s.log.Debugf("Stream built successfully, starting it")

	errChan := make(chan error, 1)
	go func() {
		s.log.Infof("Starting stream")
		errChan <- stream.Run(context.Background())
	}()

	select {
	case err := <-errChan:
		if err != nil {
			s.log.Errorf("Stream encountered an error: %v", err)
			return err
		}
	case <-time.After(100 * time.Millisecond):
		// If no error after a short delay, assume stream started successfully
	}

	s.log.Debugf("Stream started successfully")
	return nil
}

func (s *Stream) Stop() error {
	s.log.Printf("Stopping stream")

	if s.stream == nil {
		s.log.Printf("No active stream to stop")
		return nil
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- s.stream.Stop(stopCtx)
	}()

	select {
	case err := <-errChan:
		if err != nil {
			s.log.Printf("Error stopping stream: %v", err)
		} else {
			s.log.Printf("Stream stopped successfully")
		}
	case <-stopCtx.Done():
		s.log.Printf("Timeout while stopping stream")
	}

	s.streamConfig = ""
	s.stream = nil

	return nil
}

func (s *Stream) GetConfig() string {
	return s.streamConfig
}

func (s *Stream) Reset() error {
	return s.Stop()
}

func (s *Stream) GetHTTPPaths(component string) (map[string]string, error) {
	var parsedConfig map[string]interface{}
	if err := yaml.Unmarshal([]byte(s.streamConfig), &parsedConfig); err != nil {
		return nil, err
	}

	defaultPaths := map[string]map[string]string{
		"output": {
			"path":        "/get",
			"stream_path": "/get/stream",
			"ws_path":     "/get/ws",
		},
		"input": {
			"path":    "/post",
			"ws_path": "/post/ws",
		},
	}

	paths := defaultPaths[component]

	if compConfig, found := parsedConfig[component]; found {
		compMap, ok := compConfig.(map[interface{}]interface{})
		if !ok {
			return paths, nil
		}

		if http, found := compMap["http_server"]; found {
			httpMap, ok := http.(map[interface{}]interface{})
			if !ok {
				return paths, nil
			}

			for key := range paths {
				if p, found := httpMap[key]; found && p != "" {
					paths[key], _ = p.(string)
				}
			}
		}
	}

	return paths, nil
}

var unsafeComponents = []string{
	// Inputs
	"csv", "dynamic", "file", "inproc", "socket", "socket_server", "stdin", "subprocess",

	// Processors
	"command", "subprocess", "wasm",

	// Outputs
	"file", "inproc", "socket",

	// Caches
	"file",
}

func (s *Stream) removeUnsafe(yamlBytes []byte) []byte {
	filteredUnsafeComponents := []string{}

	for _, component := range unsafeComponents {
		allowed := false
		for _, allowedComponent := range s.allowedUnsafe {
			if component == allowedComponent {
				allowed = true
				break
			}
		}
		if !allowed {
			filteredUnsafeComponents = append(filteredUnsafeComponents, component)
		}
	}

	yamlString := string(yamlBytes)
	for _, key := range filteredUnsafeComponents {
		if strings.Contains(yamlString, key+":") {
			// Use regexp to match the whole block of the given key
			re := regexp.MustCompile(fmt.Sprintf(`(?m)^\s*%s:\s*(.*\r?\n?)(\s+.*(?:\r?\n?|\z))*`, regexp.QuoteMeta(key)))
			// Remove matched parts
			yamlString = re.ReplaceAllString(yamlString, "")

			s.log.Info("Removed unsafe component: ", key)
		}
	}
	return []byte(yamlString)
}