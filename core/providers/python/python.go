package python

import (
	"fmt"
	"maps"
	"regexp"
	"strings"

	"github.com/railwayapp/railpack/core/generate"
	"github.com/railwayapp/railpack/core/plan"
)

const (
	DEFAULT_PYTHON_VERSION = "3.13.2"
	UV_CACHE_DIR           = "/opt/uv-cache"
	PIP_CACHE_DIR          = "/opt/pip-cache"
	PACKAGES_DIR           = "/opt/python-packages"
	VENV_PATH              = "/app/.venv"
	LOCAL_BIN_PATH         = "/root/.local/bin"
)

type PythonProvider struct{}

func (p *PythonProvider) Name() string {
	return "python"
}

func (p *PythonProvider) Initialize(ctx *generate.GenerateContext) error {
	return nil
}

func (p *PythonProvider) Detect(ctx *generate.GenerateContext) (bool, error) {
	hasPython := ctx.App.HasMatch("main.py") ||
		p.hasRequirements(ctx) ||
		p.hasPyproject(ctx) ||
		p.hasPipfile(ctx)

	return hasPython, nil
}

func (p *PythonProvider) Plan(ctx *generate.GenerateContext) error {
	p.InstallMisePackages(ctx, ctx.GetMiseStepBuilder())

	install := ctx.NewCommandStep("install")
	install.AddInput(plan.NewStepInput(p.GetBuilderDeps(ctx).Name()))

	install.Secrets = []string{}
	install.UseSecretsWithPrefixes([]string{"PYTHON", "PIP", "PIPX", "UV", "PDM", "POETRY"})

	installOutputs := []string{}

	if p.hasRequirements(ctx) {
		installOutputs = p.InstallPip(ctx, install)
	} else if p.hasPyproject(ctx) && p.hasUv(ctx) {
		installOutputs = p.InstallUv(ctx, install)
	} else if p.hasPyproject(ctx) && p.hasPoetry(ctx) {
		installOutputs = p.InstallPoetry(ctx, install)
	} else if p.hasPyproject(ctx) && p.hasPdm(ctx) {
		installOutputs = p.InstallPDM(ctx, install)
	} else if p.hasPipfile(ctx) {
		installOutputs = p.InstallPipenv(ctx, install)
	}

	p.addMetadata(ctx)

	build := ctx.NewCommandStep("build")
	build.AddInput(plan.NewStepInput(install.Name()))

	ctx.Deploy.StartCmd = p.GetStartCommand(ctx)
	maps.Copy(ctx.Deploy.Variables, p.GetPythonEnvVars(ctx))

	ctx.Deploy.Inputs = []plan.Input{
		plan.NewStepInput(p.GetImageWithRuntimeDeps(ctx).Name()),
		plan.NewStepInput(ctx.GetMiseStepBuilder().Name(), plan.InputOptions{
			Include: ctx.GetMiseStepBuilder().GetOutputPaths(),
		}),
		plan.NewStepInput(build.Name(), plan.InputOptions{
			Include: append(installOutputs, "."),
		}),
		plan.NewLocalInput("."),
	}

	return nil
}

func (p *PythonProvider) GetStartCommand(ctx *generate.GenerateContext) string {
	startCommand := ""

	if p.isDjango(ctx) {
		startCommand = p.getDjangoStartCommand(ctx)
	}

	if startCommand == "" && ctx.App.HasMatch("main.py") {
		startCommand = "python main.py"
	}

	return startCommand
}

func (p *PythonProvider) StartCommandHelp() string {
	return "Railpack will automatically run the main.py file in the root directory as the start command."
}

func (p *PythonProvider) InstallUv(ctx *generate.GenerateContext, install *generate.CommandStepBuilder) []string {
	ctx.Logger.LogInfo("Using uv")

	install.AddCache(ctx.Caches.AddCache("uv", UV_CACHE_DIR))
	install.AddEnvVars(map[string]string{
		"UV_COMPILE_BYTECODE": "1",
		"UV_LINK_MODE":        "copy",
		"UV_CACHE_DIR":        UV_CACHE_DIR,
		"UV_PYTHON_DOWNLOADS": "never",
	})
	install.AddEnvVars(p.GetPythonEnvVars(ctx))
	install.AddCommands([]plan.Command{
		plan.NewExecCommand("pipx install uv"),
		plan.NewPathCommand(LOCAL_BIN_PATH),
		plan.NewCopyCommand("pyproject.toml"),
		plan.NewCopyCommand("uv.lock"),
		plan.NewExecCommand("uv sync --locked --no-dev --no-install-project"),
		plan.NewCopyCommand("."),
		plan.NewExecCommand("uv sync --locked --no-dev --no-editable"),
		plan.NewPathCommand(VENV_PATH + "/bin"),
	})

	return []string{}
}

func (p *PythonProvider) InstallPipenv(ctx *generate.GenerateContext, install *generate.CommandStepBuilder) []string {
	ctx.Logger.LogInfo("Using pipenv")

	install.AddEnvVars(p.GetPythonEnvVars(ctx))
	install.AddEnvVars(map[string]string{
		"PIPENV_CHECK_UPDATE":       "false",
		"PIPENV_VENV_IN_PROJECT":    "1",
		"PIPENV_IGNORE_VIRTUALENVS": "1",
	})

	install.AddCommands([]plan.Command{
		plan.NewExecCommand("pipx install pipenv"),
		plan.NewPathCommand(LOCAL_BIN_PATH),
		plan.NewPathCommand(VENV_PATH + "/bin"),
	})

	if ctx.App.HasMatch("Pipfile.lock") {
		install.AddCommands([]plan.Command{
			plan.NewCopyCommand("Pipfile"),
			plan.NewCopyCommand("Pipfile.lock"),
			plan.NewExecCommand("pipenv install --deploy --ignore-pipfile"),
		})
	} else {
		install.AddCommands([]plan.Command{
			plan.NewCopyCommand("Pipfile"),
			plan.NewExecCommand("pipenv install --skip-lock"),
		})
	}

	install.AddCommands([]plan.Command{
		plan.NewPathCommand(VENV_PATH + "/bin"),
	})

	return []string{}
}

func (p *PythonProvider) InstallPDM(ctx *generate.GenerateContext, install *generate.CommandStepBuilder) []string {
	ctx.Logger.LogInfo("Using pdm")

	install.AddEnvVars(p.GetPythonEnvVars(ctx))
	install.AddEnvVars(map[string]string{
		"PDM_CHECK_UPDATE": "false",
	})

	install.AddCommands([]plan.Command{
		plan.NewExecCommand("pipx install pdm"),
		plan.NewPathCommand(LOCAL_BIN_PATH),
		plan.NewCopyCommand("."),
		plan.NewExecCommand("python --version"),
		plan.NewExecCommand("pdm install --check --prod --no-editable"),
		plan.NewPathCommand(VENV_PATH + "/bin"),
	})

	return []string{}
}

func (p *PythonProvider) InstallPoetry(ctx *generate.GenerateContext, install *generate.CommandStepBuilder) []string {
	ctx.Logger.LogInfo("Using poetry")

	install.AddEnvVars(p.GetPythonEnvVars(ctx))

	install.AddCommands([]plan.Command{
		plan.NewExecCommand("pipx install poetry"),
		plan.NewPathCommand(LOCAL_BIN_PATH),
		plan.NewExecCommand("poetry config virtualenvs.in-project true"),
		plan.NewCopyCommand("pyproject.toml"),
		plan.NewCopyCommand("poetry.lock"),
		plan.NewExecCommand("poetry install --no-interaction --no-ansi --only main --no-root"),
		plan.NewCopyCommand("."),
		plan.NewPathCommand(VENV_PATH + "/bin"),
	})

	install.AddEnvVars(map[string]string{
		"VIRTUAL_ENV": VENV_PATH,
	})

	return []string{}
}

func (p *PythonProvider) InstallPip(ctx *generate.GenerateContext, install *generate.CommandStepBuilder) []string {
	ctx.Logger.LogInfo("Using pip")

	install.AddCache(ctx.Caches.AddCache("pip", PIP_CACHE_DIR))
	install.AddCommands([]plan.Command{
		plan.NewCopyCommand("requirements.txt"),
		plan.NewExecCommand(fmt.Sprintf("pip install --target=%s -r requirements.txt", PACKAGES_DIR)),
	})
	maps.Copy(install.Variables, p.GetPythonEnvVars(ctx))
	maps.Copy(install.Variables, map[string]string{
		"PIP_CACHE_DIR": PIP_CACHE_DIR,
		"PYTHONPATH":    PACKAGES_DIR,
	})

	return []string{PACKAGES_DIR}
}

func (p *PythonProvider) GetImageWithRuntimeDeps(ctx *generate.GenerateContext) *generate.AptStepBuilder {
	aptStep := ctx.NewAptStepBuilder("python-runtime-deps")
	aptStep.Inputs = []plan.Input{
		ctx.DefaultRuntimeInput(),
	}

	for dep, requiredPkgs := range pythonRuntimeDepRequirements {
		if p.usesDep(ctx, dep) {
			ctx.Logger.LogInfo("Installing apt packages for %s", dep)
			aptStep.Packages = append(aptStep.Packages, requiredPkgs...)
		}
	}

	if p.usesPostgres(ctx) {
		aptStep.Packages = append(aptStep.Packages, "libpq5")
	}

	return aptStep
}

func (p *PythonProvider) GetBuilderDeps(ctx *generate.GenerateContext) *generate.MiseStepBuilder {
	miseStep := ctx.GetMiseStepBuilder()
	miseStep.SupportingAptPackages = append(miseStep.SupportingAptPackages, "python3-dev", "libpq-dev")

	return miseStep
}

func (p *PythonProvider) InstallMisePackages(ctx *generate.GenerateContext, miseStep *generate.MiseStepBuilder) {
	python := miseStep.Default("python", DEFAULT_PYTHON_VERSION)

	if envVersion, varName := ctx.Env.GetConfigVariable("PYTHON_VERSION"); envVersion != "" {
		miseStep.Version(python, envVersion, varName)
	}

	if versionFile, err := ctx.App.ReadFile(".python-version"); err == nil {
		miseStep.Version(python, string(versionFile), ".python-version")
	}

	if runtimeFile, err := ctx.App.ReadFile("runtime.txt"); err == nil {
		miseStep.Version(python, string(runtimeFile), "runtime.txt")
	}

	if pipfileVersion := parseVersionFromPipfile(ctx); pipfileVersion != "" {
		miseStep.Version(python, pipfileVersion, "Pipfile")
	}

	if p.hasPoetry(ctx) || p.hasUv(ctx) || p.hasPdm(ctx) || p.hasPipfile(ctx) {
		miseStep.Default("pipx", "latest")
	}
}

func (p *PythonProvider) GetPythonEnvVars(ctx *generate.GenerateContext) map[string]string {
	return map[string]string{
		"PYTHONFAULTHANDLER":            "1",
		"PYTHONUNBUFFERED":              "1",
		"PYTHONHASHSEED":                "random",
		"PYTHONDONTWRITEBYTECODE":       "1",
		"PIP_DISABLE_PIP_VERSION_CHECK": "1",
		"PIP_DEFAULT_TIMEOUT":           "100",
	}
}

func (p *PythonProvider) usesPostgres(ctx *generate.GenerateContext) bool {
	djangoPythonRe := regexp.MustCompile(`django.db.backends.postgresql`)
	containsDjangoPostgres := len(ctx.App.FindFilesWithContent("**/*.py", djangoPythonRe)) > 0
	return p.usesDep(ctx, "psycopg2") || p.usesDep(ctx, "psycopg2-binary") || containsDjangoPostgres
}

func (p *PythonProvider) addMetadata(ctx *generate.GenerateContext) {
	hasPoetry := p.hasPoetry(ctx)
	hasPdm := p.hasPdm(ctx)
	hasUv := p.hasUv(ctx)

	pkgManager := "pip"

	if hasPoetry {
		pkgManager = "poetry"
	} else if hasPdm {
		pkgManager = "pdm"
	} else if hasUv {
		pkgManager = "uv"
	}

	ctx.Metadata.Set("pythonPackageManager", pkgManager)
	ctx.Metadata.SetBool("pythonHasRequirementsTxt", p.hasRequirements(ctx))
	ctx.Metadata.SetBool("pythonHasPyproject", p.hasPyproject(ctx))
	ctx.Metadata.SetBool("pythonHasPipfile", p.hasPipfile(ctx))
	ctx.Metadata.SetBool("pythonDjango", p.isDjango(ctx))
}

func (p *PythonProvider) usesDep(ctx *generate.GenerateContext, dep string) bool {
	for _, file := range []string{"requirements.txt", "pyproject.toml", "Pipfile"} {
		if contents, err := ctx.App.ReadFile(file); err == nil {
			// TODO: Do something better than string comparison
			if strings.Contains(strings.ToLower(contents), strings.ToLower(dep)) {
				return true
			}
		}
	}
	return false
}

var pipfileVersionRegex = regexp.MustCompile(`(python_version|python_full_version)\s*=\s*['"]([0-9.]*)"?`)

func parseVersionFromPipfile(ctx *generate.GenerateContext) string {
	pipfile, err := ctx.App.ReadFile("Pipfile")
	if err != nil {
		return ""
	}

	matches := pipfileVersionRegex.FindStringSubmatch(string(pipfile))

	if len(matches) > 2 {
		return matches[2]
	}
	return ""
}

func (p *PythonProvider) hasRequirements(ctx *generate.GenerateContext) bool {
	return ctx.App.HasMatch("requirements.txt")
}

func (p *PythonProvider) hasPyproject(ctx *generate.GenerateContext) bool {
	return ctx.App.HasMatch("pyproject.toml")
}

func (p *PythonProvider) hasPipfile(ctx *generate.GenerateContext) bool {
	return ctx.App.HasMatch("Pipfile")
}

func (p *PythonProvider) hasPoetry(ctx *generate.GenerateContext) bool {
	return ctx.App.HasMatch("poetry.lock")
}

func (p *PythonProvider) hasPdm(ctx *generate.GenerateContext) bool {
	return ctx.App.HasMatch("pdm.lock")
}

func (p *PythonProvider) hasUv(ctx *generate.GenerateContext) bool {
	return ctx.App.HasMatch("uv.lock")
}

// Mapping of python dependencies to required apt packages
var pythonRuntimeDepRequirements = map[string][]string{
	"pdf2image": {"poppler-utils"},
	"pydub":     {"ffmpeg"},
	"pymovie":   {"ffmpeg", "qt5-qmake", "qtbase5-dev", "qtbase5-dev-tools", "qttools5-dev-tools", "libqt5core5a", "python3-pyqt5"},
}
