"""Simulation layer: solvers + the experiment runner.

``ExperimentRunner`` is exposed lazily: the runner imports the objective, the
objective imports the solver, so importing the runner eagerly here would close
an import cycle (objective -> simulation -> runner -> objective).
"""

from .solver import AnalyticalVacuumSolver, DiscreteFilamentSolver, Solver

__all__ = ["AnalyticalVacuumSolver", "DiscreteFilamentSolver", "Solver", "ExperimentRunner"]


def __getattr__(name: str):
    if name == "ExperimentRunner":
        from .runner import ExperimentRunner

        return ExperimentRunner
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
