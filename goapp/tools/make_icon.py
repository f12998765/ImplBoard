#!/usr/bin/env python3
"""生成 macOS 风格的应用图标（1024×1024 PNG）。

苹果的图标规范：1024 画布留 100px 边距，主体是 824×824 的「连续曲率圆角矩形」
（superellipse，n≈5），加一点顶部高光和很淡的投影。全部用代码画，不依赖外部素材。

用法：python3 tools/make_icon.py [输出路径]
"""

import sys
from math import copysign, cos, pi, sin

from PIL import Image, ImageDraw, ImageFilter

SIZE = 1024            # 最终尺寸
SS = 4                 # 超采样倍数
W = SIZE * SS
MARGIN = 100 * SS      # 主体 824
BODY = W - 2 * MARGIN
N = 5.0                # superellipse 指数（苹果约 5）
TOP = (62, 231, 198)       # #3EE7C6
BOTTOM = (0, 158, 133)     # #009E85
GLYPH_TOP = (255, 255, 255)
GLYPH_BOTTOM = (236, 252, 248)


def squircle(size, n=N, steps=2880):
    """size×size 的 superellipse 蒙版。"""
    a = size / 2
    e = 2.0 / n
    pts = []
    for i in range(steps):
        t = 2 * pi * i / steps
        c, s = cos(t), sin(t)
        pts.append((a + a * copysign(abs(c) ** e, c), a + a * copysign(abs(s) ** e, s)))
    mask = Image.new("L", (size, size), 0)
    ImageDraw.Draw(mask).polygon(pts, fill=255)
    return mask


def vgradient(size, top, bottom, gamma=0.92):
    img = Image.new("RGB", (size, size))
    d = ImageDraw.Draw(img)
    for y in range(size):
        t = (y / (size - 1)) ** gamma
        d.line([(0, y), (size, y)],
               fill=tuple(int(top[i] * (1 - t) + bottom[i] * t) for i in range(3)))
    return img


def main():
    out = sys.argv[1] if len(sys.argv) > 1 else "resources/icon.png"

    body_mask = squircle(BODY)
    body = vgradient(BODY, TOP, BOTTOM).convert("RGBA")
    body.putalpha(body_mask)

    # 左上柔光
    sheen = Image.new("L", (BODY, BODY), 0)
    ImageDraw.Draw(sheen).ellipse([-BODY * .35, -BODY * .55, BODY * .85, BODY * .55], fill=64)
    sheen = sheen.filter(ImageFilter.GaussianBlur(BODY * .12))
    sheen = Image.composite(sheen, Image.new("L", (BODY, BODY), 0), body_mask).point(lambda v: int(v * .5))
    body = Image.composite(Image.new("RGBA", (BODY, BODY), (255, 255, 255, 255)), body, sheen)

    # 顶部内高光
    hl = Image.new("L", (BODY, BODY), 0)
    dh = ImageDraw.Draw(hl)
    for y in range(int(BODY * .5)):
        dh.line([(0, y), (BODY, y)], fill=int(44 * (1 - y / (BODY * .5)) ** 1.7))
    hl = hl.filter(ImageFilter.GaussianBlur(BODY * .03))
    hl = Image.composite(hl, Image.new("L", (BODY, BODY), 0), body_mask)
    body = Image.composite(Image.new("RGBA", (BODY, BODY), (255, 255, 255, 255)), body, hl)

    # 内侧 2px 亮边
    inner = Image.new("L", (BODY, BODY), 0)
    inner.paste(squircle(BODY - 3 * SS), (int(1.5 * SS), int(1.5 * SS)))
    ring = Image.composite(body_mask, Image.new("L", (BODY, BODY), 0),
                           Image.eval(inner, lambda v: 255 - v)).point(lambda v: int(v * .16))
    body = Image.composite(Image.new("RGBA", (BODY, BODY), (255, 255, 255, 255)), body, ring)

    # 图形：三根递增的圆头柱子
    bar_w = int(BODY * .155)
    gap = int(BODY * .078)
    total = bar_w * 3 + gap * 2
    x0 = (BODY - total) // 2
    base_y = int(BODY * .760)
    heights = (.195, .350, .505)

    gmask = Image.new("L", (BODY, BODY), 0)
    gd = ImageDraw.Draw(gmask)
    for i, h in enumerate(heights):
        x = x0 + i * (bar_w + gap)
        gd.rounded_rectangle([x, base_y - int(BODY * h), x + bar_w, base_y],
                             radius=int(bar_w * .30), fill=255)
    gimg = vgradient(BODY, GLYPH_TOP, GLYPH_BOTTOM, gamma=1.0).convert("RGBA")
    gimg.putalpha(gmask)

    gsh = Image.new("L", (BODY, BODY), 0)
    gsh.paste(gmask, (0, int(BODY * .010)))
    gsh = gsh.filter(ImageFilter.GaussianBlur(BODY * .016))
    gsh = Image.composite(gsh, Image.new("L", (BODY, BODY), 0), body_mask).point(lambda v: int(v * .20))
    body = Image.composite(Image.new("RGBA", (BODY, BODY), (2, 62, 53, 255)), body, gsh)
    body = Image.alpha_composite(body, gimg)

    # 画布：投影 + 主体
    canvas = Image.new("RGBA", (W, W), (0, 0, 0, 0))
    shadow = Image.new("L", (W, W), 0)
    shadow.paste(body_mask, (MARGIN, MARGIN + 10 * SS))
    shadow = shadow.filter(ImageFilter.GaussianBlur(18 * SS)).point(lambda v: int(v * .28))
    canvas = Image.composite(Image.new("RGBA", (W, W), (6, 44, 38, 255)), canvas, shadow)
    canvas.alpha_composite(body, (MARGIN, MARGIN))

    canvas = canvas.resize((SIZE, SIZE), Image.LANCZOS)
    canvas.save(out)
    print("wrote", out, canvas.size)


if __name__ == "__main__":
    main()
