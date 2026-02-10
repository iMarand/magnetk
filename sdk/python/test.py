import sys
import os

# Ensure sdk/python is in path
sys.path.append(os.path.dirname(os.path.abspath(__file__)))

from magnetk.link import MagnetkLink
from magnetk.client import MagnetkClient

print("--- Magnetk Python SDK Test ---")

# Test Link
link = "magnetk:?xt=urn:sha256:abc123hash&dn=testfile.txt&xl=1024&relay=/ip4/127.0.0.1/tcp/4001"
ml = MagnetkLink.parse(link)
print(f"Parsed: {ml.file_hash}, {ml.file_name}")
print(f"Generated: {str(ml)}")

if str(ml) != link:
    print("FAIL: Link roundtrip failed")
    print(f"Expected: {link}")
    print(f"Actual:   {str(ml)}")
    exit(1)
    
print("PASS: Link roundtrip")

# Test Client
client = MagnetkClient()
client.download(link, "./output.dat")
