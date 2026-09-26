"""HUSHFUSION Forge — fusion design & learning engine.

Phase 0 loop (implemented, runnable):

    parametric coil design -> Biot-Savart field -> objective -> search -> registry -> rules

The engine is deliberately built as a *design loop* rather than a device:
the asset is the ability to propose, score, record and improve the next
generation of coil configurations, not any single configuration.
"""

__version__ = "0.1.0"

from .config import MU0, Spec, Weights, ParamBounds  # noqa: F401
