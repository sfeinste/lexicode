package kernel

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// SystemModulesPath is the route this file serves. It is a constant so that the frontend contract
// and the tests refer to the same string.
const SystemModulesPath = "GET /api/v1/system/modules"

// modulesResponse is the body of GET /api/v1/system/modules. It is an object rather than a bare
// array so that later stories can add fields (a boot timestamp, the store's migration version)
// without changing the shape a client already parses.
type modulesResponse struct {
	Modules []ModuleStatus `json:"modules"`
}

// registerSystemRoutes registers the routes the kernel owns itself. Story S06 moves these onto
// kernel/httpx with the middleware chain; the path and the body stay the same.
func (k *Kernel) registerSystemRoutes() {
	k.mux.HandleFunc(SystemModulesPath, func(w http.ResponseWriter, _ *http.Request) {
		body := modulesResponse{Modules: k.Modules()}
		if body.Modules == nil {
			// Encode an empty list, never null: a client that renders "no modules" should not
			// have to distinguish the two.
			body.Modules = []ModuleStatus{}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			k.logger.Warn("could not write the module list",
				slog.String("path", "/api/v1/system/modules"), slog.String("error", err.Error()))
		}
	})
}
