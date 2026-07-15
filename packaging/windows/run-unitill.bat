@echo off
rem Universal Till POS — run from the extracted release folder.
rem Keeps the working directory here so web assets and data are found.
cd /d "%~dp0"
rem The till opens the setup page in your browser itself once it's ready.
unitill-pos.exe
