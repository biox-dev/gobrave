package docker

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"

	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// OpenTerminal 等价于 `docker exec -it <container> <command>`。
func (d *DockerRuntime) OpenTerminal(ctx context.Context, id string, opts containerruntime.TerminalOptions) (containerruntime.TerminalSession, error) {
	containerID, err := d.toContainerID(id)
	if err != nil {
		return nil, err
	}

	command := opts.Command
	if len(command) == 0 {
		command = []string{"/bin/bash"}
	}

	cli, err := d.getClient()
	if err != nil {
		return nil, err
	}

	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          command,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          opts.TTY,
	})
	if err != nil {
		return nil, fmt.Errorf("create exec for %s: %w", containerID, err)
	}

	attach, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{Tty: opts.TTY})
	if err != nil {
		return nil, fmt.Errorf("attach exec for %s: %w", containerID, err)
	}

	session := &dockerTerminalSession{
		reader: attach.Reader,
		conn:   attach.Conn,
		execID: execResp.ID,
		cli:    cli,
		close:  attach.Close,
	}
	if opts.Rows > 0 && opts.Cols > 0 {
		_ = session.Resize(opts.Rows, opts.Cols)
	}
	return session, nil
}

type dockerTerminalSession struct {
	reader  io.Reader
	conn    net.Conn
	execID  string
	cli     *client.Client
	close   func()
	closeMu sync.Once
}

func (s *dockerTerminalSession) Read(p []byte) (int, error) {
	return s.reader.Read(p)
}

func (s *dockerTerminalSession) Write(p []byte) (int, error) {
	return s.conn.Write(p)
}

func (s *dockerTerminalSession) Resize(rows, cols uint16) error {
	if s.cli == nil || rows == 0 || cols == 0 {
		return nil
	}
	return s.cli.ContainerExecResize(context.Background(), s.execID, container.ResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	})
}

func (s *dockerTerminalSession) Close() error {
	s.closeMu.Do(func() {
		if s.close != nil {
			s.close()
		}
	})
	return nil
}
