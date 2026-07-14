@echo off
rem Universal Till POS — run from the extracted release folder.
rem Keeps the working directory here so web assets and data are found.
cd /d "%~dp0"
start "" http://localhost:8080
unitill-pos.exe
