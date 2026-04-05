import './SidebarLogo.css'

const leftLogoLines = [
    "                                      .'",
    "                                     '[\"     :<",
    "                                    ',|^     ?I,",
    "                           `        : }}'^ '`(:.,",
    "                           [i       i;ncvYucYrf<,",
    "                   >l     .]^I      /JJUJUUUUUJn",
    '                   !U?<[(nf/ ,I     fJYYYUUUUXYn         ";.',
    '                   .xYUUUUXC/-nl   ;JUZ#hLUUQkdJ~      "^?\' \'.\"',
    "                    tYYUU0OUJJJv-   jvQqqLUUOhqc:    ., ,< <]'",
    "                   +hwUUp%aCCJUXU{ ']v{uYUYUUun)   '}riIj/t|`",
    "             l     'vOJULLUn{YctJY^l|f|jO1^lJt[; .<uUJUJCXJY|`",
    "            ^{      \\xvUYJc+I)1fLu}1]{{(nn[tn[];][zUYUUUUCJZ*0,",
    "             \\;    :: 1LCn~!f|(rJ11/{|]~_~[]]i{\\t1\\YCUUJuxJqZ('",
    '             ;rI   "|(nux~!xv(//}\\fY/(||?}][[/{f|/f?)vJJc; -xYCvi',
    "              :f}^  .\"_++1|(})|]f/CJU\\jt/t(trc{ftcrf}{XJ(<  >1vC|l'",
    '                _t1I  ,?]~I"(1juUJJUJccXftf/Yr)fvLU)f_>vn-" .~/_+.',
    "                 .+tr}:     ,|(cLJUUUJXJXf/xQ/(uLY\\-.  ;rzj?I`:",
    "                    inUu/+<[uXr\\fzJUJJUUJztYU\\uJr}~      ;?l`",
    "                      cJUXUUJJCJvrXJUUUUJUvJXXCn(xX^",
    "                     ]UUUUUUYXJJCJYYUJUUUUJUJJUrtXJ>",
    "                   ^|JUYJUUUUccYYUztuJUUUUUJUJzt/cc;",
    "                  >zUUYrUJYXJUXvujtf/UUJJJJUJJftrYv<",
    "                  tJYJYttzCnfJJCYtj)\\CYXuxYJz)|jUJY^",
    "                  <JUJUj)_j|/LUUnt1[tjft/tf\\i(frJJ|",
    "                  `XUUJni .ivJJr{[)jftfft(+  [ttuXl .",
    "                  .cJYJu,  >JUJY_(t\\\\||\\)i.   ~//rX/l",
    "                  _rUJJ<   IYUUJ?^1\\\\||\\\\|\\\\<     l\\\\rYJU[",
    '                 ;t/jx?    :zUUz" `)tftt~      l(tuYJ}',
    "                 ?tt{l     'rUU} . l//t+        '~|tuJ1`",
    "                >t/!        /JJf   lt/i           -//nXcI",
    "               ~t|:        .\\xnj.  ;\\t?           li-tXUr`",
    "               1t)         lt//\\~   '{t[\"  .        ')xUUv<`.",
    '              `|tt<       `(\\\\\\}\\f~   `]\\t\\))[~l    . }YcJUYcj1I"`',
    "              `r///|l    ;j/rjnj/,     .;-/\\\\\\\\/1`      !!__xUXYXXJxI",
    "              ?;    cucw|o(C <i[+_         .              .,;+)~{-^",
    "                         ` li +' `",
]

const rightLogoText = `                              .o8                            .oooooo.    .oooooo..o
                             "888                           d8P'  \`Y8b  d8P'    \`Y8
 .ooooo.   .ooooo.  oooo d8b  888oooo.   .ooooo.  oooo d8b 888      888 Y88bo.
d88' \`"Y8 d88' \`88b \`888""8P  d88' \`88b d88' \`88b \`888""8P 888      888  \`"Y8888o.
888       888ooo888  888      888   888 888ooo888  888     888      888      \`"Y88b
888   .o8 888    .o  888      888   888 888    .o  888     \`88b    d88' oo     .d8P
\`Y8bod8P' \`Y8bod8P' d888b     \`Y8bod8P' \`Y8bod8P' d888b     \`Y8bood8P'  8""88888P'`

function SidebarLogo() {
    return (
        <div className="sidebar-logo">
            <pre>{leftLogoLines.join('\n')}</pre>
            <pre className="logo-text">{rightLogoText}</pre>
        </div>
    )
}

export default SidebarLogo
