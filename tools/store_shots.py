#!/usr/bin/env python3
"""Собирает скриншоты 1280×800 для карточки в каталоге AMO.

Берёт уже снятые попапы из /tmp/shots (их делает /root/awg-browser-vpn/shots.py)
и кладёт их на тёмный холст с подписью. Размер 1280×800 — рекомендация AMO.
"""
import glob
import os
import sys

from PIL import Image, ImageDraw, ImageFont

SHOTS = "/tmp/shots"
OUT = "/root/awgbv/dist/store"

BG = (13, 13, 16)
# лёгкая подсветка в углу, чтобы кадр не выглядел плоским
GLOW = (245, 158, 11)

CAPTIONS = {
    "home": "Подключено: трафик только браузера, остальное напрямую",
    "sites": "Списки «через VPN» и «напрямую» — как в клиенте Amnezia",
    "servers": "Несколько профилей, выбор активного",
    "settings": "Состояние туннеля, автоподключение, порт прокси",
}

FONT_CANDIDATES = [
    "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
    "/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
    "/usr/share/fonts/TTF/DejaVuSans.ttf",
]


def font(size):
    for p in FONT_CANDIDATES:
        if os.path.exists(p):
            return ImageFont.truetype(p, size)
    return ImageFont.load_default()


def compose(src, caption):
    canvas = Image.new("RGB", (1280, 800), BG)
    draw = ImageDraw.Draw(canvas, "RGBA")

    # свечение по центру, за попапом: подсвечивает кадр, но не уводит композицию вбок
    cx, cy = 640, 380
    for i in range(90, 0, -1):
        r = 300 + i * 4
        alpha = int(9 * (i / 90) ** 2)
        draw.ellipse([cx - r, cy - r, cx + r, cy + r], fill=GLOW + (alpha,))

    popup = Image.open(src).convert("RGB")
    # тень под попапом
    shadow = Image.new("RGBA", (popup.width + 60, popup.height + 60), (0, 0, 0, 0))
    ImageDraw.Draw(shadow).rounded_rectangle([30, 30, popup.width + 30, popup.height + 30],
                                             radius=26, fill=(0, 0, 0, 130))
    shadow = shadow.filter(__import__("PIL.ImageFilter", fromlist=["GaussianBlur"]).GaussianBlur(18))
    px = (1280 - popup.width) // 2
    py = (800 - popup.height) // 2 - 26
    canvas.paste(shadow, (px - 30, py - 30), shadow)
    canvas.paste(popup, (px, py))

    f = font(26)
    text = caption
    w = draw.textlength(text, font=f)
    draw.text(((1280 - w) / 2, py + popup.height + 22), text, font=f, fill=(228, 228, 231))
    return canvas


def main():
    files = sorted(glob.glob(os.path.join(SHOTS, "screen-*.png")))
    if not files:
        sys.exit("нет скриншотов в /tmp/shots — сначала запусти shots.py")
    os.makedirs(OUT, exist_ok=True)
    made = 0
    for p in files:
        name = os.path.basename(p)[len("screen-"):-len(".png")]
        if name not in CAPTIONS:
            continue
        out = os.path.join(OUT, f"{name}.png")
        compose(p, CAPTIONS[name]).save(out)
        print("saved", out)
        made += 1
    print(f"итого: {made} → {OUT}")


if __name__ == "__main__":
    main()
