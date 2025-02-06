package integration_tests

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/railwayapp/railpack/buildkit"
	"github.com/railwayapp/railpack/core"
	"github.com/railwayapp/railpack/core/app"
	"github.com/stretchr/testify/require"
)

type TestCase struct {
	ExpectedOutput string            `json:"expectedOutput"`
	Envs           map[string]string `json:"envs"`
}

func runContainerWithTimeout(t *testing.T, imageName, expectedOutput string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", imageName)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start container: %v", err)
	}

	var output, errOutput strings.Builder
	done := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			output.WriteString(line + "\n")
			if strings.Contains(line, expectedOutput) {
				_ = cmd.Process.Kill()
				done <- nil
				return
			}
		}
		done <- fmt.Errorf("container output:\n%s\nErrors:\n%s", output.String(), errOutput.String())
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			errOutput.WriteString(scanner.Text() + "\n")
		}
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("container timed out after 2 minutes")
	case err := <-done:
		if err != nil {
			require.Contains(t, output.String(), expectedOutput, "container output did not contain expected string")
		}
		return err
	case err := <-cmdDoneChan(cmd):
		if err != nil && !strings.Contains(err.Error(), "signal: killed") {
			return fmt.Errorf("container failed: %v", err)
		}
		require.Contains(t, output.String(), expectedOutput, "container output did not contain expected string")
		return nil
	}
}

func cmdDoneChan(cmd *exec.Cmd) chan error {
	ch := make(chan error, 1)
	go func() { ch <- cmd.Wait() }()
	return ch
}

func TestExamplesIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	wd, err := os.Getwd()
	require.NoError(t, err)

	examplesDir := filepath.Join(filepath.Dir(wd), "examples")
	entries, err := os.ReadDir(examplesDir)
	require.NoError(t, err)

	for _, entry := range entries {
		entry := entry // capture for parallel execution
		if !entry.IsDir() {
			continue
		}

		testConfigPath := filepath.Join(examplesDir, entry.Name(), "test.json")
		if _, err := os.Stat(testConfigPath); os.IsNotExist(err) {
			continue
		}

		testConfigBytes, err := os.ReadFile(testConfigPath)
		require.NoError(t, err)

		var testCases []TestCase
		err = json.Unmarshal(testConfigBytes, &testCases)
		require.NoError(t, err)

		for i, testCase := range testCases {
			testCase := testCase // capture for parallel execution
			i := i

			testName := fmt.Sprintf("%s/case-%d", entry.Name(), i)
			t.Run(testName, func(t *testing.T) {
				t.Parallel()

				examplePath := filepath.Join(examplesDir, entry.Name())
				userApp, err := app.NewApp(examplePath)
				if err != nil {
					t.Fatalf("failed to create app: %v", err)
				}

				env := app.NewEnvironment(&testCase.Envs)
				buildResult, err := core.GenerateBuildPlan(userApp, env, &core.GenerateBuildPlanOptions{})
				if err != nil {
					t.Fatalf("failed to generate build plan: %v", err)
				}
				if buildResult == nil {
					t.Fatal("build result is nil")
				}

				imageName := fmt.Sprintf("railpack-test-%s-%s",
					strings.ToLower(strings.ReplaceAll(testName, "/", "-")),
					strings.ToLower(uuid.New().String()))

				if err := buildkit.BuildWithBuildkitClient(examplePath, buildResult.Plan, buildkit.BuildWithBuildkitClientOptions{
					ImageName: imageName,
				}); err != nil {
					t.Fatalf("failed to build image: %v", err)
				}

				if err := runContainerWithTimeout(t, imageName, testCase.ExpectedOutput); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
