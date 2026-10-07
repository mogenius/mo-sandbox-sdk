/**
 * What `codeRun()` starts a Python snippet with. It runs the snippet as `__main__` with the same argv,
 * `__file__`, exit code and traceback as `python3 file.py`, and when the snippet draws with matplotlib it prints
 * every figure as one `__mo_chart__:` line — on `plt.show()` and for what is still open at the end. Without
 * matplotlib it only runs the snippet. The Go SDK carries the same program (`packages/go/pkg/mogenius/code_run.py`);
 * a Go test keeps the two identical.
 */
export const PYTHON_BOOTSTRAP = String.raw`import os
import runpy
import sys
import traceback

MARKER = "__mo_chart__:"


def chart_type(fig):
    axes = [ax for ax in fig.axes if ax.get_visible()]
    if len(axes) != 1:
        return "composite" if axes else "unknown"
    ax = axes[0]
    from matplotlib.collections import PathCollection
    from matplotlib.container import BarContainer
    from matplotlib.patches import Wedge

    if any(isinstance(c, BarContainer) for c in ax.containers):
        return "bar"
    if any(isinstance(p, Wedge) for p in ax.patches):
        return "pie"
    if any(isinstance(c, PathCollection) for c in ax.collections):
        return "scatter"
    if ax.get_lines():
        return "line"
    return "unknown"


def chart_title(fig):
    title = fig.get_suptitle() if hasattr(fig, "get_suptitle") else ""
    for ax in fig.axes:
        title = title or ax.get_title()
    return title


def emit():
    """Prints every open figure as one chart line and closes it."""
    plt = sys.modules.get("matplotlib.pyplot")
    if plt is None:
        return
    import base64
    import io
    import json

    for num in plt.get_fignums():
        try:
            fig = plt.figure(num)
            png = io.BytesIO()
            fig.savefig(png, format="png", bbox_inches="tight")
            chart = {
                "type": chart_type(fig),
                "title": chart_title(fig),
                "png": base64.b64encode(png.getvalue()).decode("ascii"),
            }
            sys.stdout.write(MARKER + json.dumps(chart, separators=(",", ":")) + "\n")
        except Exception:
            pass
    plt.close("all")
    sys.stdout.flush()


class PyplotHook:
    """As pyplot is first imported, points matplotlib at a backend that draws
    with Agg and shows by emitting, unless MPLBACKEND decides."""

    def find_spec(self, name, path=None, target=None):
        if name == "matplotlib.pyplot":
            sys.meta_path.remove(self)
            if not os.environ.get("MPLBACKEND"):
                try:
                    import types
                    from matplotlib.backends.backend_agg import FigureCanvasAgg

                    backend = types.ModuleType("mo_charts")
                    backend.FigureCanvas = FigureCanvasAgg
                    backend.show = lambda *args, **kwargs: emit()
                    sys.modules["mo_charts"] = backend
                    sys.modules["matplotlib"].use("module://mo_charts")
                except Exception:
                    pass
        return None


def main():
    path = sys.argv[1]
    sys.argv = sys.argv[1:]
    sys.path[0] = os.path.dirname(os.path.abspath(path))
    sys.meta_path.insert(0, PyplotHook())
    try:
        runpy.run_path(path, run_name="__main__")
    except SystemExit:
        raise
    except BaseException as error:
        # the traceback starts at the script, as if it ran on its own
        tb = error.__traceback__
        while tb is not None and tb.tb_frame.f_code.co_filename != path:
            tb = tb.tb_next
        if tb is None and not isinstance(error, SyntaxError):
            tb = error.__traceback__
        traceback.print_exception(type(error), error, tb)
        sys.exit(1)
    finally:
        try:
            emit()
        except Exception:
            pass


main()
`;
