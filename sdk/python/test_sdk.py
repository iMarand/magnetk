import os
import sys
import time
from magnetk.client import MagnetkClient

def main():
    print("Testing Magnetk Python SDK...")
    
    # Check binaries
    # Assuming running from sdk/python
    bin_dir = os.path.abspath(os.path.join(os.getcwd(), "..", "..", "bin"))
    seed_exe = os.path.join(bin_dir, "seed.exe")
    
    if not os.path.exists(seed_exe):
        print(f"Warning: seed.exe not found at {seed_exe}. Ensure Go binaries are built.")
    
    # Create dummy test file
    test_file = "test_sdk.dat"
    if not os.path.exists(test_file):
        with open(test_file, "wb") as f:
            f.write(b"DATA" * 1024 * 1024) # 4MB
            
    # Initialize client with DHT enabled
    # We point relay to localhost for testing
    client = MagnetkClient({
        'dht_enabled': True,
        'relay_url': '127.0.0.1:4001',
        'auto_config': True,
        'peer_port': 5002
    })
    
    print(f"Sharing file: {test_file}")
    try:
        # Share
        link = client.share(test_file)
        print(f"Magnet Link: {link}")
        
        # Download
        print(f"Downloading from link...")
        output = "downloaded_sdk.dat"
        if os.path.exists(output):
            os.remove(output)
            
        final_path = client.get(link, output)
        print(f"Downloaded to {final_path}")
        
        # Verify
        if os.path.exists(final_path):
             print("SUCCESS: File downloaded.")
        else:
             print("FAILURE: File not found after download.")
             
    except Exception as e:
        print(f"Test failed with error: {e}")

if __name__ == "__main__":
    main()
