package server

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"maps"
	"net/http"
	"slices"
)

type helpCatalog struct {
	control map[string]jsontext.Value
	blocks  []helpBlockCatalog
}

type helpBlockCatalog struct {
	creator string
	flavors []string
	methods []namedSchema
}

type namedSchema struct {
	name string
	raw  jsontext.Value
}

type helpBlock struct {
	Creator string           `json:"creator"`
	Flavors []string         `json:"flavors"`
	Methods []jsontext.Value `json:"methods"`
}

type helpDocument struct {
	Methods    []jsontext.Value  `json:"methods"`
	BlockTypes []helpBlock       `json:"block_types"`
	HTTP       map[string]string `json:"http,omitempty"`
}

func methodName(raw jsontext.Value) (string, error) {
	var method struct {
		Name string `json:"name"`
	}
	err := jsonv2.Unmarshal(raw, &method)
	return method.Name, err
}

// SetHelp supplies an upstream-shaped schema catalog. Only registered methods
// are exposed. A registered method without a schema is listed by name.
func (s *Server) SetHelp(catalog []byte) error {
	var document helpDocument
	if err := jsonv2.Unmarshal(catalog, &document); err != nil {
		return err
	}
	parsed := helpCatalog{control: make(map[string]jsontext.Value, len(document.Methods))}
	for _, raw := range document.Methods {
		name, err := methodName(raw)
		if err != nil {
			return err
		}
		parsed.control[name] = raw
	}
	for _, block := range document.BlockTypes {
		catalog := helpBlockCatalog{creator: block.Creator, flavors: block.Flavors}
		for _, raw := range block.Methods {
			name, err := methodName(raw)
			if err != nil {
				return err
			}
			catalog.methods = append(catalog.methods, namedSchema{name: block.Creator + "." + name, raw: raw})
		}
		parsed.blocks = append(parsed.blocks, catalog)
	}
	s.mu.Lock()
	s.help = parsed
	s.mu.Unlock()
	return nil
}

func (s *Server) describe() helpDocument {
	s.mu.Lock()
	defer s.mu.Unlock()
	document := helpDocument{Methods: []jsontext.Value{}, BlockTypes: []helpBlock{}}
	blockMethods := map[string]bool{}
	for _, block := range s.help.blocks {
		methods := make([]jsontext.Value, 0, len(block.methods))
		for _, method := range block.methods {
			if _, supported := s.handlers[method.name]; supported {
				methods = append(methods, method.raw)
				blockMethods[method.name] = true
			}
		}
		if len(methods) > 0 {
			document.BlockTypes = append(document.BlockTypes, helpBlock{Creator: block.creator, Flavors: block.flavors, Methods: methods})
		}
	}
	for _, name := range slices.Sorted(maps.Keys(s.handlers)) {
		if blockMethods[name] {
			continue
		}
		raw := s.help.control[name]
		if raw == nil {
			raw, _ = jsonv2.Marshal(map[string]any{"name": name, "invoke": map[string]string{"method": name}})
		}
		document.Methods = append(document.Methods, raw)
	}
	return document
}

func (s *Server) helpAll(context.Context, *Client, json.RawMessage) (any, error) {
	return s.describe(), nil
}

func (s *Server) serveHelp(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	document := s.describe()
	document.HTTP = map[string]string{
		"control_path": "/api/control",
		"data_path":    "/api/data",
		"help_path":    "/api/help",
	}
	w.Header().Set("Content-Type", "application/json")
	if request.Method == http.MethodGet {
		jsonv2.MarshalWrite(w, document)
	}
}
