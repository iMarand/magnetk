from magnetk import MagnetkClient
import sys

def download():
    if len(sys.argv) < 2:
        print("Usage: python download.py <magnetk_link>")
        return

    magnet_link = sys.argv[1]
    
    # Initialize with DHT enabled for wider discovery
    client = MagnetkClient({
        'dht_enabled': True
    })

    print(f"Downloading {magnet_link}...")
    try:
        path = client.get(magnet_link, "./downloads/")
        print(f"Download complete! Saved to {path}")
    except Exception as e:
        print(f"Error: {e}")

if __name__ == "__main__":
    download()
