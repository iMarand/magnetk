import socket

from .link import MagnetkLink

class MagnetkClient:
    def __init__(self, config=None):
        self.config = config or {}

    def download(self, magnet_uri, output_path):
        ml = MagnetkLink.parse(magnet_uri)
        print(f"[Magnetk-Py] Starting download for: {ml.file_name}")
        print(f"[Magnetk-Py] Warning: TCP download not fully supported by Go seed (UDP-only). Protocol checks only.")
        return
