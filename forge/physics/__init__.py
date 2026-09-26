"""Physics layer: geometry parameterisation, magnetostatics, field metrics."""

from .geometry import Coil, Grids, build_grids, coils_to_vector, random_design, vector_to_coils
from .magnetic_field import (
    coilset_field,
    coilset_field_magnitude,
    loop_field,
    loop_field_discrete,
    on_axis_field,
)
from .plasma_model import axis_ripple, metrics

__all__ = [
    "Coil",
    "Grids",
    "build_grids",
    "coils_to_vector",
    "random_design",
    "vector_to_coils",
    "coilset_field",
    "coilset_field_magnitude",
    "loop_field",
    "loop_field_discrete",
    "on_axis_field",
    "axis_ripple",
    "metrics",
]
