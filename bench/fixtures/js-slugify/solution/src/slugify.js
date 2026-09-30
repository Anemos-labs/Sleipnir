'use strict';

// A separator is one or more ASCII punctuation characters: ! " # $ % & ' ( ) * + , - . / : ; < = > ? @ [ \ ] ^ _ ` { | } ~
const SEPARATOR = /^[!-/:-@[-`{-~]+$/;

function slugify(text, options = {}) {
  if (typeof text !== 'string') {
    throw new TypeError('text must be a string');
  }
  const { separator = '-', maxLength } = options;
  if (typeof separator !== 'string' || !SEPARATOR.test(separator)) {
    throw new TypeError('separator must be a non-empty string of ASCII punctuation characters');
  }
  if (maxLength !== undefined && !(Number.isInteger(maxLength) && maxLength >= 1)) {
    throw new RangeError('maxLength must be a positive integer');
  }

  const words = text
    .normalize('NFKD')
    .replace(/\p{M}+/gu, '')
    .toLowerCase()
    .match(/[a-z0-9]+/g) ?? [];

  let slug = words.join(separator);
  if (maxLength !== undefined && slug.length > maxLength) {
    // Cut anywhere, then drop what is left of a separator at the end.
    slug = slug.slice(0, maxLength).replace(/[^a-z0-9]+$/, '');
  }
  return slug;
}

module.exports = { slugify };
