#!/usr/bin/env python3
"""Synthesize an SRT narration into a timeline-aligned, mono WAV with Piper."""
import argparse
import io
import re
import subprocess
import wave
from pathlib import Path

from piper import PiperVoice, SynthesisConfig


PRONUNCIATIONS = {
    'amd64': 'A M D sixty-four',
    'arm64': 'Arm sixty-four',
    'cgo': 'see go',
    'dlls': 'D L Ls',
    'gil': 'gee eye ell',
    'wsgi': 'whisky',
}
INTERNALS_PRONUNCIATIONS = {
    **PRONUNCIATIONS,
    'asyncio': 'async I O',
    'config.slots': 'config dot slots',
    'cpython': 'C Python',
    'crosscall2': 'cross call two',
    'fastcall': 'fast call',
    'getaddrinfo': 'get address info',
    'goaio': 'Go A I O',
    'goeventloop': 'Go Event Loop',
    'goselectoreventloop': 'Go Selector Event Loop',
    'newfunc': 'new func',
    'pthread': 'P thread',
    'py.do': 'pie dot do',
    'py.newfunc': 'pie dot new func',
    'py.release': 'pie dot release',
    'syscalln': 'syscall N',
}
NATIVE_PRONUNCIATIONS = {
    **INTERNALS_PRONUNCIATIONS,
    '__real_': 'double underscore real',
    '__real_pthread_create': 'double underscore real P thread create',
    '__wrap_pthread_create': 'double underscore wrap P thread create',
    'bao': 'bough',
    'dll': 'D L L',
    'eagain': 'E again',
    'elf': 'elf',
    'getprocaddress': 'get proc address',
    'libc': 'lib C',
    'libpython': 'lib Python',
    'lld': 'L L D',
    'llvm': 'L L V M',
    'lto': 'L T O',
    'mach-o': 'mock O',
    'pthread_create': 'P thread create',
    'pthread_exit': 'P thread exit',
    'pthread_self': 'P thread self',
    'threadstart': 'thread start',
    'tls': 'T L S',
}
PRONUNCIATION_TERMS = re.compile(
    r'\b(?:' + '|'.join(map(re.escape, PRONUNCIATIONS)) + r')\b', re.IGNORECASE,
)
INTERNALS_TERMS = re.compile(
    r'\b(?:' + '|'.join(map(re.escape, INTERNALS_PRONUNCIATIONS)) + r')\b', re.IGNORECASE,
)
NATIVE_TERMS = re.compile(
    r'\b(?:' + '|'.join(map(re.escape, NATIVE_PRONUNCIATIONS)) + r')\b', re.IGNORECASE,
)


def spoken_text(text, internals=False, native=False):
    if native:
        return NATIVE_TERMS.sub(lambda match: NATIVE_PRONUNCIATIONS[match[0].lower()], text)
    if internals:
        return INTERNALS_TERMS.sub(lambda match: INTERNALS_PRONUNCIATIONS[match[0].lower()], text)
    return PRONUNCIATION_TERMS.sub(lambda match: PRONUNCIATIONS[match[0].lower()], text)


def seconds(stamp):
    hours, minutes, rest = stamp.split(':')
    return int(hours) * 3600 + int(minutes) * 60 + float(rest.replace(',', '.'))


def cues_from_srt(path):
    text = Path(path).read_text(encoding='utf-8').strip()
    for block in re.split(r'\n\s*\n', text):
        lines = block.splitlines()
        start, end = lines[1].split(' --> ')
        yield seconds(start), seconds(end), ' '.join(lines[2:])


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--model', required=True, help='Piper ONNX model; its .json must be alongside it')
    ap.add_argument('--srt', default=str(Path(__file__).with_name('narration.srt')))
    ap.add_argument('--output', required=True)
    guide = ap.add_mutually_exclusive_group()
    guide.add_argument('--internals', action='store_true', help='Use the runtime companion pronunciation guide')
    guide.add_argument('--native', action='store_true', help='Use the native internals pronunciation guide')
    args = ap.parse_args()
    cues = list(cues_from_srt(args.srt))
    if not cues:
        ap.error('The subtitle file contains no narration cues')
    voice = PiperVoice.load(args.model)
    rate = voice.config.sample_rate
    # Leave a short breath at either end of each cue, compressing only if needed.
    output = bytearray(round(cues[-1][1] * rate) * 2)
    for i, (start, end, text) in enumerate(cues, 1):
        stream = io.BytesIO()
        with wave.open(stream, 'wb') as wav:
            voice.synthesize_wav(spoken_text(text, args.internals, args.native), wav,
                                 syn_config=SynthesisConfig(length_scale=1.0))
        with wave.open(io.BytesIO(stream.getvalue()), 'rb') as wav:
            if wav.getframerate() != rate or wav.getnchannels() != 1 or wav.getsampwidth() != 2:
                raise ValueError('Unexpected Piper audio format')
            pcm = wav.readframes(wav.getnframes())
        available = end - start - 0.35
        length = len(pcm) / (2 * rate)
        if length > available:
            factor = length / available
            if factor > 2:
                raise ValueError(f'Cue {i} needs an unreasonable speed increase: {factor:.2f}x')
            proc = subprocess.run(['ffmpeg', '-loglevel', 'error', '-f', 's16le', '-ar', str(rate), '-ac', '1', '-i', 'pipe:0', '-filter:a', f'atempo={factor:.6f}', '-f', 's16le', 'pipe:1'], input=pcm, capture_output=True, check=True)
            pcm = proc.stdout
        offset = round((start + .15) * rate) * 2
        limit = round((end - .1) * rate) * 2
        if offset + len(pcm) > limit:
            raise ValueError(f'Cue {i} does not fit its window')
        output[offset:offset + len(pcm)] = pcm
        print(f'{i:02d}/{len(cues)}  {start:06.1f}-{end:06.1f}  speech {length:.2f}s', flush=True)
    with wave.open(args.output, 'wb') as wav:
        wav.setnchannels(1)
        wav.setsampwidth(2)
        wav.setframerate(rate)
        wav.writeframes(output)


if __name__ == '__main__':
    main()
