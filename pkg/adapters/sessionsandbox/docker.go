package sessionsandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/provideroutput"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/providerprocess"
)

const ownerLabel = "net.openlinker.session-owner"

func (session *Session) connect(ctx context.Context) error {
	path, err := exec.LookPath("docker")
	if err != nil {
		return errors.New("Docker session isolation requires the Docker CLI and a local Linux daemon")
	}
	session.docker = path
	session.env = providerprocess.Environment(os.Environ(), []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "XDG_RUNTIME_DIR"})
	// Remote daemons interpret bind paths on a different host. Do not let a
	// misconfigured context silently mount somebody else's session directory.
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint == "" || os.Getenv("DOCKER_CONTEXT") != "" {
		endpoint, err = session.control(ctx, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
		if err != nil {
			return err
		}
	}
	if !strings.HasPrefix(endpoint, "unix:///") {
		return errors.New("session isolation requires a local Unix-socket Docker context")
	}
	// Freeze the resolved endpoint for every operation in this invocation. A
	// concurrent `docker context use` must not redirect create/start/cleanup.
	stableEnv := make([]string, 0, len(session.env)+1)
	for _, entry := range session.env {
		if !strings.HasPrefix(entry, "DOCKER_HOST=") && !strings.HasPrefix(entry, "DOCKER_CONTEXT=") {
			stableEnv = append(stableEnv, entry)
		}
	}
	session.env = append(stableEnv, "DOCKER_HOST="+endpoint)
	engineInfo, err := session.control(ctx, "info", "--format", `{"os":{{json .OSType}},"id":{{json .ID}}}`)
	if err != nil {
		return err
	}
	var engine struct {
		OS string `json:"os"`
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(engineInfo), &engine) != nil || engine.OS != "linux" || strings.TrimSpace(engine.ID) == "" {
		return errors.New("session isolation requires an available Linux Docker daemon")
	}
	session.engine = engine.ID
	info, err := session.control(ctx, "image", "inspect", "--format", `{"os":{{json .Os}},"volumes":{{json (index .Config "Volumes")}}}`, session.config.Image)
	if err != nil {
		return fmt.Errorf("session image is unavailable locally; install the pinned image before starting Node: %w", err)
	}
	var image struct {
		OS      string                     `json:"os"`
		Volumes map[string]json.RawMessage `json:"volumes"`
	}
	if json.Unmarshal([]byte(info), &image) != nil || image.OS != "linux" || len(image.Volumes) != 0 {
		return errors.New("session image must be Linux and must not declare additional volumes")
	}
	if network := session.config.Network; network != "" && network != "none" {
		driver, err := session.control(ctx, "network", "inspect", "--format", "{{.Driver}}", network)
		if err != nil {
			return err
		}
		if driver != "bridge" {
			return errors.New("SESSION_NETWORK must select an existing Docker bridge network")
		}
	}
	return nil
}

func (session *Session) control(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, session.docker, args...)
	providerprocess.Configure(command)
	command.Env = session.env
	stdout, stderr := provideroutput.NewLimitedBuffer(cancel), provideroutput.NewLimitedBuffer(cancel)
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("Docker session operation failed; check the local daemon and session image")
	}
	if err := provideroutput.LimitError("Docker", stdout, stderr); err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (session *Session) removeContainer() error {
	// Use a new bounded context even when the Attempt was cancelled.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ids, err := session.control(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+session.name+"$")
	if err != nil || ids == "" {
		return err
	}
	if strings.ContainsAny(ids, "\r\n ") {
		return errors.New("ambiguous session container identity")
	}
	owner, err := session.control(ctx, "container", "inspect", "--format", `{{index .Config.Labels "`+ownerLabel+`"}}`, ids)
	if err != nil || owner != session.owner {
		return errors.New("refusing to remove a container not owned by this session")
	}
	_, err = session.control(ctx, "container", "rm", "--force", ids)
	return err
}

// Command creates the container before attaching its stdio. Separating create
// from start lets cancellation always remove the whole process tree, including
// a detached grandchild. No host shell, host executable, or host HOME is mounted.
func (session *Session) Command(ctx context.Context, bin string, args, environment []string) (*exec.Cmd, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.lock == nil {
		return nil, errors.New("session sandbox is closed")
	}
	if strings.TrimSpace(bin) == "" || strings.HasPrefix(bin, "-") {
		return nil, errors.New("session provider executable is required inside the image")
	}
	if err := session.removeContainer(); err != nil {
		return nil, err
	}
	network := session.config.Network
	if network == "" {
		network = "none"
	}
	create := []string{"container", "create", "--interactive", "--init", "--pull=never", "--name", session.name,
		"--label", ownerLabel + "=" + session.owner, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--pids-limit=256", "--memory=2g", "--memory-swap=2g", "--cpus=2", "--ipc=private", "--cgroupns=private",
		"--network", network, "--log-driver=none", "--no-healthcheck", "--restart=no",
		"--user", fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()), "--workdir", Workspace,
		"--mount", "type=bind,src=" + session.data + ",dst=/session,bind-propagation=rprivate",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=268435456,mode=1777", "--entrypoint", bin}
	// This short-lived private file is outside the session mount. Values never
	// enter argv, and guest HOME/PATH do not replace the Docker CLI environment.
	seen := make(map[string]bool)
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || seen[key] || strings.ContainsAny(entry, "\r\n\x00") {
			return nil, errors.New("invalid or duplicate session client environment")
		}
		if strings.HasPrefix(key, "OPENLINKER_") || strings.HasPrefix(key, "DOCKER_") {
			return nil, errors.New("platform and Docker control environment cannot enter a session")
		}
		seen[key] = true
	}
	file, err := os.CreateTemp(session.root, ".client-env-*")
	if err != nil {
		return nil, errors.New("cannot prepare private session environment")
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(strings.Join(environment, "\n") + "\n")
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return nil, errors.New("cannot write private session environment")
	}
	create = append(create, "--env-file", file.Name())
	create = append(create, session.config.Image)
	create = append(create, args...)
	id, err := session.control(ctx, create...)
	if err != nil {
		return nil, err
	}
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return nil, errors.New("Docker returned an invalid session container ID")
	}
	command := exec.CommandContext(ctx, session.docker, "container", "start", "--attach", "--interactive", id)
	command.Env = session.env
	providerprocess.Configure(command)
	return command, nil
}
