import os
import io
import json
import hashlib
import datetime
from pathlib import Path

import boto3
from tqdm import tqdm
from google.oauth2.credentials import Credentials
from googleapiclient.discovery import build
from googleapiclient.http import MediaIoBaseDownload

# ---------- Config ----------
FOLDER_ID = os.environ["DRIVE_FOLDER_ID"]
BASE_URL = os.environ["R2_PUBLIC_BASE_URL"].rstrip("/") + "/"
WORKDIR = Path("drive_cache")
PAGES = Path("pages")
STATE_FILE = WORKDIR / ".state.json"

WORKDIR.mkdir(exist_ok=True)
PAGES.mkdir(exist_ok=True)

# ---------- Google auth ----------
creds = Credentials(
    None,
    refresh_token=os.environ["GOOGLE_REFRESH_TOKEN"],
    token_uri="https://oauth2.googleapis.com/token",
    client_id=os.environ["GOOGLE_CLIENT_ID"],
    client_secret=os.environ["GOOGLE_CLIENT_SECRET"],
    scopes=["https://www.googleapis.com/auth/drive.readonly"],
)

drive = build("drive", "v3", credentials=creds, cache_discovery=False)

# ---------- R2 ----------
s3 = boto3.client(
    "s3",
    endpoint_url=os.environ["R2_ENDPOINT"],
    aws_access_key_id=os.environ["R2_ACCESS_KEY_ID"],
    aws_secret_access_key=os.environ["R2_SECRET_ACCESS_KEY"],
)
BUCKET = os.environ["R2_BUCKET"]

# ---------- Load previous state ----------
if STATE_FILE.exists():
    state = json.loads(STATE_FILE.read_text())
else:
    state = {}

# ---------- List Drive files ----------
files = {}
page_token = None

while True:
    resp = drive.files().list(
        q=f"'{FOLDER_ID}' in parents and trashed=false",
        fields="nextPageToken, files(id,name,size,modifiedTime,md5Checksum)",
        pageSize=1000,
        pageToken=page_token,
    ).execute()

    for f in resp["files"]:
        files[f["id"]] = f

    page_token = resp.get("nextPageToken")
    if not page_token:
        break

# ---------- Delta detection ----------
to_download = []
seen_ids = set()

for fid, f in files.items():
    seen_ids.add(fid)
    prev = state.get(fid)

    if not prev or prev["md5"] != f.get("md5Checksum"):
        to_download.append(f)

# ---------- Download changed files ----------
for f in tqdm(to_download, desc="Downloading"):
    path = WORKDIR / f["name"]
    request = drive.files().get_media(fileId=f["id"])
    fh = io.FileIO(path, "wb")
    downloader = MediaIoBaseDownload(fh, request)

    done = False
    while not done:
        _, done = downloader.next_chunk()

# ---------- Upload to R2 ----------
for f in tqdm(to_download, desc="Uploading"):
    path = WORKDIR / f["name"]
    s3.upload_file(
        str(path),
        BUCKET,
        f["name"],
        ExtraArgs={"ACL": "public-read"},
    )

# ---------- Remove deleted files ----------
deleted = set(state.keys()) - seen_ids
for fid in deleted:
    name = state[fid]["name"]
    s3.delete_object(Bucket=BUCKET, Key=name)

# ---------- Write new state + filelist ----------
new_state = {}
filelist = []

for fid, f in files.items():
    name = f["name"]
    size = int(f.get("size", 0))
    md5 = f.get("md5Checksum", "")
    modified = f["modifiedTime"]

    new_state[fid] = {"name": name, "md5": md5}

    filelist.append(
        {
            "path": name,
            "size": size,
            "sha256": hashlib.sha256(
                (WORKDIR / name).read_bytes()
                if (WORKDIR / name).exists()
                else md5.encode()
            ).hexdigest(),
            "modified": modified,
        }
    )

STATE_FILE.write_text(json.dumps(new_state, indent=2))

(PAGES / "filelist.json").write_text(
    json.dumps(
        {
            "generated": datetime.datetime.utcnow().isoformat() + "Z",
            "base_url": BASE_URL,
            "files": sorted(filelist, key=lambda x: x["path"]),
        },
        indent=2,
    )
)

print("Sync complete.")
