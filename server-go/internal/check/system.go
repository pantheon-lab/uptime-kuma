package check

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

const (
	systemdBusName = "org.freedesktop.systemd1"
	systemdPath    = dbus.ObjectPath("/org/freedesktop/systemd1")
	commandTimeout = 5 * time.Second
	maxOutputRunes = 200
)

// unitSuffixes are the unit types systemctl recognizes. A name without one is a service.
var unitSuffixes = []string{".service", ".socket", ".device", ".mount", ".automount", ".swap", ".target", ".path",
	".timer", ".slice", ".scope"}

// systemBus connects to the system's D-Bus. It's a variable so that tests can use their own bus.
var systemBus = func(ctx context.Context) (*dbus.Conn, error) {
	return dbus.ConnectSystemBus(dbus.WithContext(ctx))
}

// runSystemService asks systemd whether a unit is active, as `systemctl is-active` does. It talks
// to systemd over D-Bus instead of running systemctl, so that it also works in a container that
// has the host's system bus socket mounted.
func runSystemService(ctx context.Context, m monitor.Monitor) monitor.Result {
	name := strings.TrimSpace(m.SystemServiceName)
	state, err := unitActiveState(ctx, name)
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	// is-active succeeds for these two, and prints the state otherwise.
	if state == "active" || state == "reloading" {
		return monitor.Result{OK: true, Msg: fmt.Sprintf("Service '%s' is running.", name)}
	}
	return monitor.Result{Msg: state}
}

func unitActiveState(ctx context.Context, name string) (string, error) {
	unit := name
	if !hasUnitSuffix(unit) {
		unit += ".service"
	}
	conn, err := systemBus(ctx)
	if err != nil {
		return "", fmt.Errorf("can't reach systemd over D-Bus (%w). In a container, mount the host's /run/dbus/system_bus_socket", err)
	}
	defer func() { _ = conn.Close() }()
	var path dbus.ObjectPath
	err = conn.Object(systemdBusName, systemdPath).CallWithContext(ctx, systemdBusName+".Manager.GetUnit", 0, unit).Store(&path)
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) && dbusErr.Name == systemdBusName+".NoSuchUnit" {
		// systemd only loads units that are in use, so this one is inactive or doesn't exist.
		return "inactive", nil
	}
	if err != nil {
		return "", fmt.Errorf("ask systemd about %s: %w", unit, err)
	}
	value, err := conn.Object(systemdBusName, path).GetProperty(systemdBusName + ".Unit.ActiveState")
	if err != nil {
		return "", fmt.Errorf("read the state of %s: %w", unit, err)
	}
	state, isString := value.Value().(string)
	if !isString {
		return "", fmt.Errorf("systemd sent an unexpected state for %s", unit)
	}
	return state, nil
}

func hasUnitSuffix(name string) bool {
	for _, suffix := range unitSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// PM2Process is one process in PM2's list.
type PM2Process struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// pm2Command runs `pm2 jlist`. It's a variable so that tests can fake PM2.
var pm2Command = func(ctx context.Context) ([]byte, []byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "pm2", "jlist")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// PM2ProcessList lists PM2's processes by running `pm2 jlist`, as the Node.js backend did.
func PM2ProcessList(ctx context.Context) ([]PM2Process, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	stdout, stderr, err := pm2Command(ctx)
	if err != nil {
		details := truncateOutput(stderr)
		if details == "" {
			if errors.Is(err, exec.ErrNotFound) {
				details = "ENOENT"
			} else {
				details = err.Error()
			}
		}
		return nil, fmt.Errorf("Unable to query PM2 process list (%s).", details) //nolint:staticcheck // Shown to users, as in Node.js.
	}
	var raw []struct {
		PMID   *json.Number `json:"pm_id"`
		Name   string       `json:"name"`
		PM2Env *struct {
			Status any `json:"status"`
		} `json:"pm2_env"`
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		if details := truncateOutput(stderr); details != "" {
			return nil, errors.New(details)
		}
		return nil, errors.New("Unable to parse PM2 process list output.") //nolint:staticcheck // Shown to users, as in Node.js.
	}
	list := []PM2Process{}
	for _, item := range raw {
		id := ""
		if item.PMID != nil {
			id = item.PMID.String()
		}
		if id == "" && item.Name == "" {
			continue
		}
		status := "unknown"
		if item.PM2Env != nil && item.PM2Env.Status != nil {
			status = strings.ToLower(fmt.Sprint(item.PM2Env.Status))
		}
		list = append(list, PM2Process{ID: cmp.Or(id, item.Name), Name: cmp.Or(item.Name, id), Status: status})
	}
	return list, nil
}

func truncateOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len([]rune(text)) > maxOutputRunes {
		return string([]rune(text)[:maxOutputRunes]) + "..."
	}
	return text
}

// runPM2 is UP when the process, by name or id, is online.
func runPM2(ctx context.Context, m monitor.Monitor) monitor.Result {
	name := strings.TrimSpace(m.SystemServiceName)
	list, err := PM2ProcessList(ctx)
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	for _, process := range list {
		if process.Name != name && process.ID != name {
			continue
		}
		if process.Status == "online" {
			return monitor.Result{OK: true, Msg: fmt.Sprintf("PM2 process '%s' is online.", name)}
		}
		return monitor.Result{Msg: fmt.Sprintf("PM2 process '%s' is %s.", name, process.Status)}
	}
	return monitor.Result{Msg: fmt.Sprintf("PM2 process '%s' was not found.", name)}
}
