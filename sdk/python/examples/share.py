from magnetk import MagnetkClient
import sys
import time

def share():
    client = MagnetkClient({
        'relay_url': '69.169.109.243:4003',
        'dht_enabled': True,
        # 'dht_bootstrap_peers': [] # Optional
    })

    file_path = "./examples/test_file.txt"
    try:
        print(f"Sharing {file_path}...")
        link = client.share(file_path)
        
        print("-" * 50)
        print("FILE SHARED SUCCESSFULLY!")
        print(f"Magnetk Link: {link}")
        print("-" * 50)
        
        print("Seeder running in background. Press Ctrl+C to stop.")
        while True:
            time.sleep(1)
            
    except Exception as e:
        print(f"Error: {e}")

if __name__ == "__main__":
    share()
