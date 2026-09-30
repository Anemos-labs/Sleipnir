'use strict';
// Acceptance tests for csvtool: the CSV format, head and select. Every expectation follows from README.md.

const { ok, fails, group, PEOPLE, ANN, BOB, PEOPLE_HEADER } = require('./helpers');

// ------------------------------------------------------------------ reading and writing CSV (head N as identity)

group(
  'the CSV format: what is read and what is written',
  [
    ['plain', 'a,b\n1,2\n', 'a,b\n1,2\n'],
    ['no final newline', 'a,b\n1,2', 'a,b\n1,2\n'],
    ['crlf', 'a,b\r\n1,2\r\n', 'a,b\n1,2\n'],
    ['empty lines are skipped', '\na,b\n\n1,2\n\n\n3,4\n\n', 'a,b\n1,2\n3,4\n'],
    ['empty crlf lines are skipped', '\r\na,b\r\n\r\n1,2\r\n', 'a,b\n1,2\n'],
    ['quotes that are not needed are dropped', '"a","b"\n"1","2"\n', 'a,b\n1,2\n'],
    [
      'quotes that are needed are written',
      'a,b\n"x,y","he said ""hi"""\n"line1\nline2",z\n',
      'a,b\n"x,y","he said ""hi"""\n"line1\nline2",z\n',
    ],
    ['crlf inside a quoted field is data', 'a,b\r\n"x\r\ny",z\r\n', 'a,b\n"x\r\ny",z\n'],
    ['empty fields', 'a,b,c\n,,\n1,,3\n', 'a,b,c\n,,\n1,,3\n'],
    ['a trailing comma at the end of the input', 'a,b\n1,', 'a,b\n1,\n'],
    ['a lone empty field is quoted', 'a\n""\nx\n', 'a\n""\nx\n'],
    ['an empty line is not a value', 'a\n\nx\n', 'a\nx\n'],
    ['an empty quoted field among others', 'a,b\n"",x\n', 'a,b\n,x\n'],
    ['non-ASCII text', 'nom,ville\nZoë,Zürich\n日本,東京\n', 'nom,ville\nZoë,Zürich\n日本,東京\n'],
    ['a line of spaces is a value', 'a\n   \n', 'a\n   \n'],
    ['a value that is a double quote', 'a\n""""\n', 'a\n""""\n'],
    ['a quoted empty last field at the end of the input', 'a,b\n1,""', 'a,b\n1,\n'],
  ],
  (input, expected) => ok(['head', '1000'], input, expected),
);


// ------------------------------------------------------------------ head

group(
  'head',
  [
    ['the first two rows', ['head', '2'], PEOPLE, PEOPLE_HEADER + ANN + BOB],
    ['zero rows: the header only', ['head', '0'], PEOPLE, PEOPLE_HEADER],
    ['more than there are', ['head', '10'], PEOPLE, PEOPLE],
    ['leading zeros', ['head', '007'], PEOPLE, PEOPLE],
    ['a huge N', ['head', '99999999999999999999'], PEOPLE, PEOPLE],
  ],
  (args, input, expected) => ok(args, input, expected),
);

group(
  'head: N must be a non-negative integer',
  ['-1', '1.5', 'abc', '', '1e3'].map((n) => [JSON.stringify(n), n]),
  (n) => fails(['head', n], PEOPLE, 2, 'csvtool: head: N must be a non-negative integer\n'),
);


// ------------------------------------------------------------------ select

group(
  'select',
  [
    ['one column', ['select', 'name'], PEOPLE, 'name\nAnn\nBob\n"Cy ""the Kid"""\nDee\n'],
    ['two columns, reordered', ['select', 'city,name'], PEOPLE, 'city,name\n"Paris, FR",Ann\nRome,Bob\nOslo,"Cy ""the Kid"""\nRome,Dee\n'],
    ['a column listed twice', ['select', 'age,age'], PEOPLE, 'age,age\n31,31\n25,25\n40,40\n25,25\n'],
    ['a name with a space', ['select', 'first name,age'], '"first name",age\nAnn,3\n', 'first name,age\nAnn,3\n'],
    ['a lone empty field is quoted', ['select', 'a'], 'a,b\n,x\ny,z\n', 'a\n""\ny\n'],
    ['when two columns have the same name the first is meant', ['select', 'a'], 'a,b,a\n1,2,3\n', 'a\n1\n'],
    ['only a header', ['select', 'b,a'], 'a,b\n', 'b,a\n'],
  ],
  (args, input, expected) => ok(args, input, expected),
);

group(
  'select: unknown columns',
  [
    ['one unknown', ['select', 'nope'], 'csvtool: unknown column: nope\n'],
    ['the first unknown one is reported', ['select', 'name,nope,zzz'], 'csvtool: unknown column: nope\n'],
    ['names are case-sensitive', ['select', 'Name'], 'csvtool: unknown column: Name\n'],
    ['names are not trimmed', ['select', 'name, age'], 'csvtool: unknown column:  age\n'],
  ],
  (args, stderr) => fails(args, PEOPLE, 1, stderr),
);
