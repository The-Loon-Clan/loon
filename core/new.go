package core

import (
	"fmt"
	"log/slog"
	"strings"
)

// Deps carries every service New requires to assemble a Core.
// The field set mirrors Core exactly — see the field docs there.
// Every field is REQUIRED: New reports all missing fields in one
// error so the composition root (cmd/main.go) fixes the wiring in
// one pass instead of playing whack-a-mole.
//
// Tests that only exercise a slice of Core may construct a
// &Core{} literal directly; production code MUST come through New
// so a half-wired mediator can never reach a plugin's Provision.
type Deps struct {
	// Process is the process kind this Core serves: "web",
	// "worker", or "all". Drives Boot's plugin filter.
	Process string

	Users         UsersService
	Auth          AuthService
	RBAC          RBACService
	Storage       StorageService
	Scheduler     SchedulerService
	Router        RouterService
	Logger        *slog.Logger
	Config        ConfigService
	Notifications NotificationsService
	Points        PointsService
	HTTPClient    HTTPClientService
	Errors        ErrorReporter
}

// New validates d and assembles the Core mediator. It fails loud:
// any nil field is an error, and the error names every missing
// field. cmd/main.go treats the error as fatal — a plugin that
// nil-panics at request time because Core.Users was never set is
// strictly worse than refusing to boot.
func New(d Deps) (*Core, error) {
	var missing []string
	req := func(name string, ok bool) {
		if !ok {
			missing = append(missing, name)
		}
	}
	req("Users", d.Users != nil)
	req("Auth", d.Auth != nil)
	req("RBAC", d.RBAC != nil)
	req("Storage", d.Storage != nil)
	req("Scheduler", d.Scheduler != nil)
	req("Router", d.Router != nil)
	req("Logger", d.Logger != nil)
	req("Config", d.Config != nil)
	req("Notifications", d.Notifications != nil)
	req("Points", d.Points != nil)
	req("HTTPClient", d.HTTPClient != nil)
	req("Errors", d.Errors != nil)
	switch d.Process {
	case "web", "worker", "all":
	default:
		missing = append(missing, fmt.Sprintf("Process (got %q, want web|worker|all)", d.Process))
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("core: New missing required deps: %s", strings.Join(missing, ", "))
	}
	return &Core{
		Process:       d.Process,
		Users:         d.Users,
		Auth:          d.Auth,
		RBAC:          d.RBAC,
		Storage:       d.Storage,
		Scheduler:     d.Scheduler,
		Router:        d.Router,
		Logger:        d.Logger,
		Config:        d.Config,
		Notifications: d.Notifications,
		Points:        d.Points,
		HTTPClient:    d.HTTPClient,
		Errors:        d.Errors,
	}, nil
}
