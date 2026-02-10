import urllib.parse

class MagnetkLink:
    def __init__(self, file_hash, file_name, file_size=0, seed_id="", relay_addr=""):
        self.file_hash = file_hash
        self.file_name = file_name
        self.file_size = int(file_size)
        self.seed_id = seed_id
        self.relay_addr = relay_addr

    @staticmethod
    def parse(uri):
        if not uri.startswith("magnetk:?"):
            raise ValueError("Invalid magnet link: must start with magnetk:?")
        
        # Remove 'magnetk:?' prefix
        query = uri[9:]
        params = urllib.parse.parse_qs(query)
        
        xt = params.get('xt', [None])[0]
        if not xt or not xt.startswith("urn:sha256:"):
            raise ValueError("Invalid magnet link: missing sha256 hash")
        
        file_hash = xt[11:]
        file_name = params.get('dn', [None])[0] or "unknown_file"
        file_size = params.get('xl', ["0"])[0]
        seed_id = params.get('xs', [""])[0]
        relay_addr = params.get('relay', [""])[0]
        
        return MagnetkLink(file_hash, file_name, file_size, seed_id, relay_addr)

    def __str__(self):
        parts = []
        parts.append(f"xt=urn:sha256:{self.file_hash}")
        if self.file_name:
            parts.append(f"dn={urllib.parse.quote(self.file_name)}")
        if self.file_size > 0:
            parts.append(f"xl={self.file_size}")
        if self.seed_id:
            parts.append(f"xs={urllib.parse.quote(self.seed_id)}")
        if self.relay_addr:
            parts.append(f"relay={self.relay_addr}")
            
        return f"magnetk:?{'&'.join(parts)}"
