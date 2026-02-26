# AA2 Hardlinker

![Preview image](./assets/preview.png)

## Disclaimer
> [!NOTE]
> Large chunks of the code were written with the help of AI. The code is messy but I went with it because I wanted to get the project done as quickly as possible.
> I've reviewed all the generated code and made sure it works, but I haven't cleaned it up or refactored it. If you want to contribute to the project, please feel free to clean up the code and make it more readable.

## About
This is a tool for downloading and updating additional clothing textures for the game. It is based on the existing work done by the community.

## Installation
1. Download the latest release for your OS from the [releases page](https://github.com/R00taryEnginE/AA2Hardlinker/releases/latest).
2. Extract the program to the same directory where your game is installed.
3. Run the program and follow the instructions.

## Troubleshooting
### Program fails to start
> [!IMPORTANT]
> On Linux the program requires `libwebkit` to be installed. Use one of the following commands to install it.
```bash
# For Debian/Ubuntu-based distributions
sudo apt install libwebkit2gtk-4.1-0
# For Fedora-based distributions
sudo dnf install webkit2gtk4.1
# For Arch-based distributions
sudo pacman -S webkit2gtk-4.1
```
### Why does the program ask for admin privileges on Windows?
This is a limitation of how the privileges for symlinks work on Windows. The program requires administrator privileges or developer mode to be turned on to be able to manage symlinks.
This StackOverflow answer explains it in more detail: https://stackoverflow.com/a/64992080/13570473

## In the weeds
So how does all of this work in detail? Until now, the "hardlinker" mod was just a large archive of textures along with a `.bat` or `.sh` script that would create symlinks
to trick the game into seeing the textures.

The entire process was manual, prone to errors, and required the user to download the entire archive every time there was an update. This is no longer the case with this program.
Here is how it works now:
1. [This script](./scripts/sync_drive.py) runs nightly via github actions and mirrors the contents of the Google Drive to a public Cloudflare R2 bucket, generating a manifest file and a symlink path mapping file using the existing `.sh` script as reference in the process.
2. The program uses the manifest file to check for updates and only downloads the files that have changed since the last update, saving bandwidth and time.
3. The program performs integrity checks on the downloaded files to ensure they are not corrupted and that they match the expected hash values.
4. The program creates the necessary symlinks to the downloaded files in the game directory, allowing the game to access the new textures.

## Credits
A huge thanks to the following people, without whom this project would not be possible:
- @clickonflareblitz - For maintaining the original hardlinker.
- @egarim and @ce00fded - For beta testing and providing feedback.
