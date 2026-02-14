"""
Magnetk Link Parser and Generator for Magnetk P2P.
Supports DHT discovery and multi-relay configuration.
"""

from urllib.parse import urlencode, parse_qs, quote, unquote


class MagnetkLink:
    """Represents a parsed Magnetk magnet link."""

    def __init__(self, file_hash='', file_name='', file_size=0,
                 seed_id='', relay_addr='', dht_enabled=False):
        self.file_hash = file_hash
        self.file_name = file_name
        self.file_size = file_size
        self.seed_id = seed_id
        self.relay_addr = relay_addr
        self.dht_enabled = dht_enabled

    @staticmethod
    def parse(uri: str) -> 'MagnetkLink':
        """
        Parse a magnetk URI string.
        
        Format: magnetk:?xt=urn:sha256:<hash>&dn=<name>&xl=<size>&xs=<seed_id>&relay=<addr>&dht=true
        """
        if not uri.startswith('magnetk:?'):
            raise ValueError('Invalid magnet link: must start with magnetk:?')

        query = uri[9:]
        params = parse_qs(query)

        # Parse xt (required)
        xt_list = params.get('xt', [''])
        xt = xt_list[0] if xt_list else ''
        if not xt.startswith('urn:sha256:'):
            raise ValueError('Invalid magnet link: missing xt (urn:sha256:...)')

        file_hash = xt.split(':')[2]
        file_name = unquote(params.get('dn', ['unknown_file'])[0])
        file_size = int(params.get('xl', ['0'])[0])
        seed_id = unquote(params.get('xs', [''])[0])
        relay_addr = params.get('relay', [''])[0]
        dht_param = params.get('dht', [''])[0].lower()
        dht_enabled = dht_param in ('true', '1')

        # Must have at least one discovery method
        if not relay_addr and not dht_enabled:
            raise ValueError('Magnet link needs either relay address or dht=true for discovery')

        return MagnetkLink(
            file_hash=file_hash,
            file_name=file_name,
            file_size=file_size,
            seed_id=seed_id,
            relay_addr=relay_addr,
            dht_enabled=dht_enabled,
        )

    def __str__(self) -> str:
        """Generate a magnetk URI string."""
        parts = [f'xt=urn:sha256:{self.file_hash}']

        if self.file_name:
            parts.append(f'dn={quote(self.file_name)}')
        if self.file_size > 0:
            parts.append(f'xl={self.file_size}')
        if self.seed_id:
            parts.append(f'xs={quote(self.seed_id)}')
        if self.relay_addr:
            parts.append(f'relay={self.relay_addr}')
        if self.dht_enabled:
            parts.append('dht=true')

        return f'magnetk:?{"&".join(parts)}'

    @property
    def has_relay(self) -> bool:
        return bool(self.relay_addr)

    @property
    def has_dht(self) -> bool:
        return self.dht_enabled
