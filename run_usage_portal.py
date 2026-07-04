#!/usr/bin/env python3
"""Run the CPA usage self-service portal."""
from __future__ import annotations

import uvicorn

from cpa_usage_portal import create_app, load_config_from_env


def main() -> None:
    cfg = load_config_from_env()
    uvicorn.run(create_app(cfg), host=cfg.host, port=cfg.port)


if __name__ == "__main__":
    main()
