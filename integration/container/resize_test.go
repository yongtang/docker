package container // import "github.com/docker/docker/integration/container"

import (
	"io"
	"sync"
	"context"
	"net/http"
	"testing"
	"time"
	"encoding/json"
"fmt"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/versions"
	"github.com/docker/docker/integration/internal/container"
	"github.com/docker/docker/internal/test/request"
	req "github.com/docker/docker/internal/test/request"
	"gotest.tools/assert"
	is "gotest.tools/assert/cmp"
	"gotest.tools/poll"
	"gotest.tools/skip"
)

func TestResize(t *testing.T) {
	defer setupTest(t)()
	client := request.NewAPIClient(t)
	ctx := context.Background()

	cID := container.Run(t, ctx, client)

	poll.WaitOn(t, container.IsInState(ctx, client, cID, "running"), poll.WithDelay(100*time.Millisecond))

	err := client.ContainerResize(ctx, cID, types.ResizeOptions{
		Height: 40,
		Width:  40,
	})
	assert.NilError(t, err)
}

func TestResizeWithInvalidSize(t *testing.T) {
	skip.If(t, versions.LessThan(testEnv.DaemonAPIVersion(), "1.32"), "broken in earlier versions")
	defer setupTest(t)()
	client := request.NewAPIClient(t)
	ctx := context.Background()

	cID := container.Run(t, ctx, client)

	poll.WaitOn(t, container.IsInState(ctx, client, cID, "running"), poll.WithDelay(100*time.Millisecond))

	endpoint := "/containers/" + cID + "/resize?h=foo&w=bar"
	res, _, err := req.Post(endpoint)
	assert.NilError(t, err)
	assert.Check(t, is.DeepEqual(http.StatusBadRequest, res.StatusCode))
}

func TestResizeWhenContainerNotStarted(t *testing.T) {
	defer setupTest(t)()
	client := request.NewAPIClient(t)
	ctx := context.Background()

	cID := container.Run(t, ctx, client, container.WithCmd("echo"))

	poll.WaitOn(t, container.IsInState(ctx, client, cID, "exited"), poll.WithDelay(100*time.Millisecond))

	err := client.ContainerResize(ctx, cID, types.ResizeOptions{
		Height: 40,
		Width:  40,
	})
	assert.Check(t, is.ErrorContains(err, "is not running"))
}

// Part of #14845
func TestExecResizeImmediatelyAfterExecStart(t *testing.T) {
	defer setupTest(t)()
        client := request.NewAPIClient(t)
        ctx := context.Background()

        cID := container.Run(t, ctx, client, /*container.WithCmd("/bin/sh"), container.WithTty(true),*/ func(c *container.TestContainerConfig) {
                        c.HostConfig.RestartPolicy.Name = "always"
                })

	//name := "exec_resize_test"
	//dockerCmd(c, "run", "-d", "-i", "-t", "--name", name, "--restart", "always", "busybox", "/bin/sh")

	testExecResize := func() error {
		data := map[string]interface{}{
			"AttachStdin": true,
			"Cmd":         []string{"/bin/sh"},
		}
		uri := fmt.Sprintf("/containers/%s/exec", cID)
		res, body, err := request.Post(uri, request.JSONBody(data))
		if err != nil {
			return err
		}
		if res.StatusCode != http.StatusCreated {
			return fmt.Errorf("POST %s is expected to return %d, got %d", uri, http.StatusCreated, res.StatusCode)
		}

		buf, err := request.ReadBody(body)
		assert.NilError(t, err)
		//c.Assert(err, checker.IsNil)

		out := map[string]string{}
		err = json.Unmarshal(buf, &out)
		if err != nil {
			return fmt.Errorf("ExecCreate returned invalid json. Error: %q", err.Error())
		}

		execID := out["Id"]
		if len(execID) < 1 {
			return fmt.Errorf("ExecCreate got invalid execID")
		}

		payload := bytes.NewBufferString(`{"Tty":true}`)
		conn, _, err := sockRequestHijack("POST", fmt.Sprintf("/exec/%s/start", execID), payload, "application/json", daemonHost())
		if err != nil {
			return fmt.Errorf("Failed to start the exec: %q", err.Error())
		}
		defer conn.Close()

		_, rc, err := request.Post(fmt.Sprintf("/exec/%s/resize?h=24&w=80", execID), request.ContentType("text/plain"))
		if err != nil {
			// It's probably a panic of the daemon if io.ErrUnexpectedEOF is returned.
			if err == io.ErrUnexpectedEOF {
				return fmt.Errorf("The daemon might have crashed.")
			}
			// Other error happened, should be reported.
			return fmt.Errorf("Fail to exec resize immediately after start. Error: %q", err.Error())
		}

		rc.Close()

		return nil
	}

	// The panic happens when daemon.ContainerExecStart is called but the
	// container.Exec is not called.
	// Because the panic is not 100% reproducible, we send the requests concurrently
	// to increase the probability that the problem is triggered.
	var (
		n  = 10
		ch = make(chan error, n)
		wg sync.WaitGroup
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := testExecResize(); err != nil {
				ch <- err
			}
		}()
	}

	wg.Wait()
	select {
	case err := <-ch:
		c.Fatal(err.Error())
	default:
	}
}
