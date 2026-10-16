import base64
import glob
import os
import pytest

from pathlib import Path


def pytest_addoption(parser):
    parser.addoption(
        "--backend-test-host",
        help="Backed address for test host",
        default="http://kicad.localhost",
    )
    parser.addoption(
        "--kicad-cli",
        help=(
            "Local kicad-cli command used for DRC checks. By default DRC runs "
            "kicad-cli in the worker image (see --drc-image), so it uses the "
            "worker's KiCad version"
        ),
        default=None,
    )
    parser.addoption(
        "--drc-image",
        help="Docker image with kicad-cli used for DRC checks",
        default=f"admwscki/keyboard-tools-kicad:{os.environ.get('TAG', 'latest')}",
    )


def is_circleci():
    return "CI" in os.environ


@pytest.fixture(scope="session")
def backend_host(request):
    return request.config.getoption("--backend-test-host")


@pytest.fixture(scope="session")
def kicad_cli(request):
    """Command prefix running kicad-cli, None means the --drc-image container."""
    return request.config.getoption("--kicad-cli")


@pytest.fixture(scope="session")
def drc_image(request):
    return request.config.getoption("--drc-image")


@pytest.fixture(scope="session")
def pcb_endpoint(request):
    value = request.config.getoption("--backend-test-host")
    return f"{value}/api/pcb"


def to_base64(path):
    with open(path, "rb") as f:
        return base64.b64encode(f.read()).decode("utf-8")


@pytest.hookimpl(hookwrapper=True)
def pytest_runtest_makereport(item, call):
    pytest_html = item.config.pluginmanager.getplugin("html")
    outcome = yield
    report = outcome.get_result()
    extras = getattr(report, "extras", [])

    if report.when == "teardown":
        try:
            tmpdir = Path(item.funcargs["tmpdir"])
        except KeyError:
            tmpdir = Path.cwd()
        images = glob.glob(f"{tmpdir}/*svg")
        for f in images:
            encoded = to_base64(f)
            extras.append(pytest_html.extras.image(f"data:image/svg+xml;base64,{encoded}"))
        report.extras = extras

