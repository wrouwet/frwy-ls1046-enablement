#!/usr/bin/env python3
"""Shared serial console for the FRWY-LS1046A.

Shows the board's console in this terminal and accepts keyboard input, like a
terminal emulator. Everything received is also appended to a log file, and
anything written to the input FIFO is sent to the board, so a second program
can follow and drive the same console. Ctrl-] quits.

Usage: console.py [port] [baud]
"""

import os
import select
import sys
import termios
import tty

import serial

PORT = sys.argv[1] if len(sys.argv) > 1 else '/dev/ttyUSB0'
BAUD = int(sys.argv[2]) if len(sys.argv) > 2 else 115200
DIR = os.path.dirname(os.path.abspath(__file__))
LOG = os.path.join(DIR, 'console.log')
FIFO = os.path.join(DIR, 'console.in')
QUIT = b'\x1d'  # Ctrl-]


def open_port():
    """Opens the port, waiting for it while the board is powered off."""
    import time
    while True:
        try:
            return serial.Serial(PORT, BAUD, timeout=0)
        except serial.SerialException:
            time.sleep(0.5)


def main() -> None:
    if not os.path.exists(FIFO):
        os.mkfifo(FIFO)
    fifo = os.open(FIFO, os.O_RDWR | os.O_NONBLOCK)
    log = open(LOG, 'ab', buffering=0)
    stdin = sys.stdin.fileno()
    saved = termios.tcgetattr(stdin)
    tty.setraw(stdin)
    print(f'[console on {PORT} at {BAUD}, logging to {LOG}; Ctrl-] quits]\r')
    ser = open_port()
    try:
        while True:
            try:
                ready, _, _ = select.select([ser.fileno(), stdin, fifo], [], [], 1)
                if ser.fileno() in ready:
                    data = ser.read(4096)
                    os.write(sys.stdout.fileno(), data)
                    log.write(data)
                if stdin in ready:
                    data = os.read(stdin, 1024)
                    if QUIT in data:
                        break
                    ser.write(data)
                if fifo in ready:
                    ser.write(os.read(fifo, 4096))
            except (serial.SerialException, OSError):
                # The USB bridge is powered by the board: wait for it to return.
                print('\r\n[port lost; waiting for the board]\r')
                ser.close()
                ser = open_port()
                print('[port back]\r')
    finally:
        termios.tcsetattr(stdin, termios.TCSADRAIN, saved)


if __name__ == '__main__':
    main()
