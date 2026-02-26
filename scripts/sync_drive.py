import argparse
import datetime
import io
import json
import os
import time
import re
from collections import deque
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Deque, Dict, Iterable, List, Optional, Sequence, Tuple

import boto3
from botocore.client import BaseClient
import requests
from tqdm import tqdm
from googleapiclient.discovery import build
from googleapiclient.errors import HttpError

PNG_MIME = "image/png"
FOLDER_MIME = "application/vnd.google-apps.folder"
LIST_FIELDS = "nextPageToken, files(id,name,size,modifiedTime,md5Checksum,mimeType)"
PLAN_FILENAME = ".sync_plan.json"
DEFAULT_DOWNLOAD_RETRIES = 3
DEFAULT_TIMEOUT = 60

@dataclass
class SyncConfig:
    folder_id: str
    base_url: Optional[str]
    workdir: Path
    plan_file: Path
    max_workers: int
    chunk_size: int
    download_retries: int
    request_timeout: int
    drive_api_key: str
    r2_bucket: str
    r2_endpoint: str
    filelist_key: str
    mapping_file_id: str
    mapping_object_key: str


def require_env(name: str) -> str:
    value = os.environ.get(name)
    if not value:
        raise RuntimeError(f"Missing required environment variable: {name}")
    return value


def load_config() -> SyncConfig:
    workdir = Path(os.environ.get("DRIVE_WORKDIR", "drive_cache"))
    plan_override = os.environ.get("SYNC_PLAN_FILE")
    plan_path = Path(plan_override) if plan_override else workdir / PLAN_FILENAME

    drive_api_key = os.environ.get("DRIVE_API_KEY")
    if not drive_api_key:
        raise RuntimeError("Missing required environment variable: DRIVE_API_KEY")

    config = SyncConfig(
        folder_id=require_env("DRIVE_FOLDER_ID"),
        base_url=os.environ.get("R2_PUBLIC_BASE_URL"),
        workdir=workdir,
        plan_file=plan_path,
        max_workers=int(os.environ.get("DOWNLOAD_WORKERS", "4")),
        chunk_size=int(os.environ.get("DOWNLOAD_CHUNK_SIZE", str(8 * 1024 * 1024))),
        download_retries=int(os.environ.get("DOWNLOAD_RETRIES", str(DEFAULT_DOWNLOAD_RETRIES))),
        request_timeout=int(os.environ.get("DOWNLOAD_TIMEOUT", str(DEFAULT_TIMEOUT))),
        drive_api_key=drive_api_key,
        r2_bucket=require_env("R2_BUCKET"),
        r2_endpoint=require_env("R2_ENDPOINT"),
        filelist_key=os.environ.get("R2_FILELIST_KEY", "filelist.json"),
        mapping_file_id=require_env("DRIVE_BASH_SCRIPT_ID"),
        mapping_object_key=os.environ.get("R2_MAPPING_KEY", "pathmap.json"),
    )

    config.workdir.mkdir(parents=True, exist_ok=True)
    config.plan_file.parent.mkdir(parents=True, exist_ok=True)
    return config


def build_drive_service(api_key: str) -> Any:
    return build(
        "drive",
        "v3",
        developerKey=api_key,
        cache_discovery=False,
        static_discovery=False,
    )


def build_s3_client(cfg: SyncConfig) -> BaseClient:
    return boto3.client(
        "s3",
        endpoint_url=cfg.r2_endpoint,
        aws_access_key_id=require_env("R2_ACCESS_KEY_ID"),
        aws_secret_access_key=require_env("R2_SECRET_ACCESS_KEY"),
    )


def execute_with_backoff(request: Any, retries: int = 5, base_delay: float = 1.0) -> Any:
    for attempt in range(retries):
        try:
            return request.execute(num_retries=0)
        except HttpError as exc:
            if attempt == retries - 1:
                raise
            delay = base_delay * (2 ** attempt)
            status = getattr(exc, "status_code", None) or getattr(exc.resp, "status", "?")
            print(f"Drive API error {status}; retrying in {delay:.1f}s")
            time.sleep(delay)


def iter_folder_entries(drive: Any, folder_id: str) -> Iterable[dict]:
    page_token = None
    query = (
        f"'{folder_id}' in parents and trashed=false and "
        f"(mimeType='{FOLDER_MIME}' or mimeType='{PNG_MIME}')"
    )

    while True:
        request = drive.files().list(
            q=query,
            fields=LIST_FIELDS,
            pageSize=1000,
            pageToken=page_token,
            includeItemsFromAllDrives=True,
            supportsAllDrives=True,
        )
        response = execute_with_backoff(request)
        for entry in response.get("files", []):
            yield entry

        page_token = response.get("nextPageToken")
        if not page_token:
            break


def discover_png_files(drive: Any, root_folder_id: str) -> Dict[str, dict]:
    queue: Deque[Tuple[str, Path]] = deque([(root_folder_id, Path())])
    discovered: Dict[str, dict] = {}

    while queue:
        folder_id, prefix = queue.popleft()
        print(f"Scanning folder: {prefix.as_posix() or '(root)'}")
        for entry in iter_folder_entries(drive, folder_id):
            mime = entry.get("mimeType", "")
            name = entry.get("name", "")

            if mime == FOLDER_MIME:
                queue.append((entry["id"], prefix / name))
                continue

            path = (prefix / name).as_posix()
            discovered[entry["id"]] = {
                "id": entry["id"],
                "path": path,
                "size": int(entry.get("size") or 0),
                "md5": entry.get("md5Checksum", ""),
                "modified": entry.get("modifiedTime"),
            }
            print(f"  queued PNG: {path}")

    return discovered


def stream_download(file_meta: dict, cfg: SyncConfig) -> None:
    path = cfg.workdir / file_meta["path"]
    path.parent.mkdir(parents=True, exist_ok=True)
    temp_path = path.with_suffix(path.suffix + ".tmp")
    url = f"https://www.googleapis.com/drive/v3/files/{file_meta['id']}?alt=media&key={cfg.drive_api_key}"

    for attempt in range(cfg.download_retries):
        headers: Dict[str, str] = {}
        try:
            with requests.get(
                url,
                headers=headers,
                stream=True,
                timeout=cfg.request_timeout,
            ) as response:
                response.raise_for_status()
                with temp_path.open("wb") as fh:
                    for chunk in response.iter_content(chunk_size=cfg.chunk_size):
                        if chunk:
                            fh.write(chunk)
            temp_path.replace(path)
            return
        except requests.HTTPError as exc:
            status = exc.response.status_code if exc.response else "?"
            print(
                f"Download HTTP error for {file_meta['path']} (status {status}) - "
                f"attempt {attempt + 1}/{cfg.download_retries}"
            )
        except (requests.RequestException, OSError) as exc:
            print(
                f"Download error for {file_meta['path']}: {exc} - "
                f"attempt {attempt + 1}/{cfg.download_retries}"
            )
        finally:
            if temp_path.exists():
                try:
                    temp_path.unlink()
                except FileNotFoundError:
                    pass

        if attempt < cfg.download_retries - 1:
            wait = 2 ** attempt
            time.sleep(wait)

    raise RuntimeError(f"Failed to download {file_meta['path']} after {cfg.download_retries} attempts")


def download_changed_files(changed: Sequence[dict], cfg: SyncConfig) -> None:
    if not changed:
        print("No Drive downloads required.")
        return

    print(f"Downloading {len(changed)} PNG files with {cfg.max_workers} workers...")
    errors: List[str] = []
    with ThreadPoolExecutor(max_workers=cfg.max_workers) as executor:
        futures = {executor.submit(stream_download, meta, cfg): meta for meta in changed}
        for future in tqdm(
            as_completed(futures),
            total=len(futures),
            desc="Downloading",
            unit="file",
        ):
            try:
                future.result()
            except Exception as exc:
                target = futures[future]["path"]
                print(f"Download failed for {target}: {exc}")
                errors.append(target)

    if errors:
        raise RuntimeError(f"Aborted after download failures: {', '.join(errors)}")


def list_r2_pngs(client: BaseClient, bucket: str) -> List[Dict[str, Any]]:
    paginator = client.get_paginator("list_objects_v2")
    files: List[Dict[str, Any]] = []

    for page in paginator.paginate(Bucket=bucket):
        for obj in page.get("Contents", []):
            key = obj.get("Key", "")
            if not key.lower().endswith(".png"):
                continue
            etag = (obj.get("ETag") or "").strip('"')
            modified = obj.get("LastModified")
            modified_iso = None
            if isinstance(modified, datetime.datetime):
                modified_iso = modified.astimezone(datetime.timezone.utc).isoformat().replace("+00:00", "Z")
            files.append(
                {
                    "path": key,
                    "size": obj.get("Size", 0),
                    "md5": etag,
                    "modified": modified_iso,
                }
            )

    files.sort(key=lambda entry: entry["path"])
    return files


def determine_sync_plan(
    drive_by_path: Dict[str, dict],
    r2_by_path: Dict[str, dict],
    force_full_upload: bool = False,
) -> Tuple[List[dict], List[str]]:
    to_upload: List[dict] = []
    for path, meta in drive_by_path.items():
        remote = r2_by_path.get(path)
        if force_full_upload or not remote or remote.get("md5") != meta.get("md5"):
            to_upload.append(meta)

    to_delete = [path for path in r2_by_path.keys() if path not in drive_by_path]
    to_upload.sort(key=lambda meta: meta["path"])
    to_delete.sort()
    return to_upload, to_delete


def write_sync_plan(path: Path, uploads: List[dict], deletes: List[str]) -> None:
    payload = {
        "generated": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
        "upload": [meta["path"] for meta in uploads],
        "delete": deletes,
    }
    path.write_text(json.dumps(payload, indent=2))


def upload_files(client: BaseClient, cfg: SyncConfig, uploads: Sequence[dict]) -> None:
    if not uploads:
        print("No R2 uploads required.")
        return

    print(f"Uploading {len(uploads)} files to R2…")
    for meta in tqdm(uploads, desc="Uploading to R2"):
        local_path = cfg.workdir / meta["path"]
        if not local_path.exists():
            raise FileNotFoundError(f"Expected local file missing: {local_path}")
        client.upload_file(
            str(local_path),
            cfg.r2_bucket,
            meta["path"],
            ExtraArgs={"ACL": "public-read"},
        )


def delete_files(client: BaseClient, cfg: SyncConfig, deletes: Sequence[str]) -> None:
    if not deletes:
        print("No R2 deletions required.")
        return

    print(f"Deleting {len(deletes)} files from R2…")
    for key in tqdm(deletes, desc="Deleting from R2"):
        client.delete_object(Bucket=cfg.r2_bucket, Key=key)


def build_filelist_payload(base_url: Optional[str], files: List[Dict[str, Any]]) -> Dict[str, Any]:
    return {
        "generated": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
        "base_url": base_url,
        "files": files,
    }


def write_filelist(cfg: SyncConfig, files: List[Dict[str, Any]]) -> Dict[str, Any]:
    payload = build_filelist_payload(cfg.base_url, files)
    output_path = cfg.workdir / "filelist.json"
    output_path.write_text(json.dumps(payload, indent=2))
    return payload


def upload_filelist_to_r2(client: BaseClient, cfg: SyncConfig, payload: Dict[str, Any]) -> None:
    client.put_object(
        Bucket=cfg.r2_bucket,
        Key=cfg.filelist_key,
        Body=json.dumps(payload, indent=2).encode("utf-8"),
        ACL="public-read",
        ContentType="application/json",
    )


def download_drive_file_bytes(cfg: SyncConfig, file_id: str) -> bytes:
    url = f"https://www.googleapis.com/drive/v3/files/{file_id}?alt=media&key={cfg.drive_api_key}"

    for attempt in range(cfg.download_retries):
        headers: Dict[str, str] = {}
        try:
            with requests.get(
                url,
                headers=headers,
                stream=True,
                timeout=cfg.request_timeout,
            ) as response:
                if response.status_code == 401:
                    force_refresh = True
                    raise requests.HTTPError(response=response)
                response.raise_for_status()
                buffer = io.BytesIO()
                for chunk in response.iter_content(chunk_size=cfg.chunk_size):
                    if chunk:
                        buffer.write(chunk)
                return buffer.getvalue()
        except requests.HTTPError as exc:
            status = exc.response.status_code if exc.response else "?"
            print(
                f"Mapping download HTTP error (status {status}) - "
                f"attempt {attempt + 1}/{cfg.download_retries}"
            )
        except requests.RequestException as exc:
            print(
                f"Mapping download error: {exc} - "
                f"attempt {attempt + 1}/{cfg.download_retries}"
            )

        if attempt < cfg.download_retries - 1:
            time.sleep(2 ** attempt)

    raise RuntimeError("Failed to download mapping source after retries")


def build_mapping_document(source_bytes: bytes):
    pattern = r'^ln\s+-s\s+\"(.*)\"\s+(.*)'
    mapping = []
    # Write byes to file
    temp_path = Path("mapping_source.tmp")
    temp_path.write_bytes(source_bytes)
    
    with temp_path.open("r", encoding="utf-8") as f:
        lines = f.readlines()
    
    for line in lines:
        match = re.match(pattern, line.strip())
        if match:
            source, target = match.groups()
            mapping.append({"source": source, "target": target})

    temp_path.unlink()
    return mapping


def upload_mapping_to_r2(client: BaseClient, cfg: SyncConfig, mapping_path: Path) -> None:
    client.put_object(
        Bucket=cfg.r2_bucket,
        Key=cfg.mapping_object_key,
        Body=mapping_path.read_bytes(),
        ACL="public-read",
        ContentType="application/json",
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Sync Google Drive PNGs into R2 storage and publish filelist metadata",
    )
    parser.add_argument(
        "--force-plan",
        action="store_true",
        help="Treat every Drive file as needing upload regardless of R2 state",
    )
    return parser.parse_args()


def main(force_plan: bool = False) -> None:
    cfg = load_config()
    drive = build_drive_service(cfg.drive_api_key)
    s3_client = build_s3_client(cfg)

    drive_files = discover_png_files(drive, cfg.folder_id)
    drive_by_path = {meta["path"]: meta for meta in drive_files.values()}

    r2_inventory = list_r2_pngs(s3_client, cfg.r2_bucket)
    r2_by_path = {entry["path"]: entry for entry in r2_inventory}

    uploads, deletes = determine_sync_plan(
        drive_by_path,
        r2_by_path,
        force_full_upload=force_plan,
    )
    write_sync_plan(cfg.plan_file, uploads, deletes)

    download_changed_files(uploads, cfg)
    upload_files(s3_client, cfg, uploads)
    delete_files(s3_client, cfg, deletes)

    final_inventory = list_r2_pngs(s3_client, cfg.r2_bucket)
    payload = write_filelist(cfg, final_inventory)
    upload_filelist_to_r2(s3_client, cfg, payload)

    print(f"Generating {cfg.mapping_object_key} from harderlinker script source file...")
    mapping_bytes = download_drive_file_bytes(cfg, cfg.mapping_file_id)
    mapping_payload = build_mapping_document(mapping_bytes)
    mapping_path = cfg.workdir / cfg.mapping_object_key
    mapping_path.write_text(json.dumps(mapping_payload, indent=2))
    upload_mapping_to_r2(s3_client, cfg, mapping_path)

    print("Drive → R2 sync complete.")


if __name__ == "__main__":
    args = parse_args()
    main(force_plan=args.force_plan)
